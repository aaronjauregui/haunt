package ghost

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aaronjauregui/haunt/internal/brain"
	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/memory"
	"github.com/aaronjauregui/haunt/internal/model"
	"github.com/aaronjauregui/haunt/internal/proto"
	"github.com/aaronjauregui/haunt/internal/skill"
	"github.com/aaronjauregui/haunt/skills"
)

// The model brain is what a ghost with no [brain] section runs on: its
// judgment is the person's model, reached directly over the chat
// completions API (internal/model), and everything here is the driver
// deciding what reaches it and putting what comes back on screen.
//
// It is a brain.Runner hosted in the driver, but it does not speak
// intents. What the model wants done arrives as decisions (decide.go) and
// is performed under the same gates as everything else; what it says is
// streamed into the balloon here. The division of labour is the one
// docs/dev/architecture.md draws: the driver owns effects — who pays for a
// wake, whether the balloon may show, what the person sees — and the model
// owns what to make of an event and whether to say anything at all.
//
// One turn runs at a time. The person's words queue behind a turn in
// flight. Something unprompted that arrives meanwhile waits too, if it is
// still worth raising once the turn is over (see deferLocked); what is only
// true in the moment — the person going idle, a neighbour's remark — is
// dropped, because by then it is stale and the ghost has just been talking.
type modelBrain struct {
	g      *Ghost
	client model.Client
	cfg    model.Config

	tr  *transcript
	mem memory.Store // nil when the ghost has no state directory
	// memDir is where the memory files are, and memDB the state.db they
	// are indexed into.
	memDir string
	memDB  *sql.DB

	// learned is where the model's own skills live; pkgSkills are the
	// package's. The host is opened again whenever what the ghost is
	// offered changes, since a skill is hidden when a tool it needs is not
	// on offer (skillHost).
	learned   string
	pkgSkills fs.FS
	skillMu   sync.Mutex
	skillKey  string
	skills    skill.Host
	warned    map[string]bool // hidden package skills already logged, under skillMu

	turns chan *turn
	busy  atomic.Bool
	done  chan struct{}

	// deferred is what arrived during a turn and waits for the ghost to be
	// free, oldest first. deferMu also covers the moment a turn ends, so an
	// event cannot be put here just after the worker last looked.
	deferMu  sync.Mutex
	deferred []deferredTurn

	// Only the bus's delivery goroutine touches these, one event at a time.
	lastPoke    time.Time
	pokeN       int
	lastReacted time.Time

	// waking is the indicator a ghost shows from the moment it is on screen
	// until it has said hello: the boot turn is seconds on a slow model, and
	// a ghost that sits there silent through it reads as broken.
	waking atomic.Pointer[waiter]
}

var _ brain.Runner = (*modelBrain)(nil)

// deferredTurn is an unprompted turn waiting for the one in flight.
type deferredTurn struct {
	// key names what it is about; a newer turn with the same key replaces
	// it, since only the latest word on one subject is worth sending.
	key  string
	turn *turn
	at   time.Time
}

// Waiting unprompted turns: how many, and for how long. Past the age, the
// moment has gone; an errand is still carried on every turn's context
// (turnContext), so a reminder that expires here is not lost.
const (
	maxDeferred = 4
	deferMaxAge = 10 * time.Minute
)

// turnQueue bounds the person's words waiting behind a turn. Past it,
// somebody is typing faster than any model answers, and the oldest-first
// order would be answering things they have stopped caring about.
const turnQueue = 8

// Lingers: long enough to read.
const (
	lingerPoke  = 4 * time.Second
	lingerReply = 12 * time.Second
	lingerError = 8 * time.Second
)

// thinkingText is what the waiting indicator says for a turn someone is
// waiting on, and wakingText what it says while the ghost says hello.
const (
	thinkingText = "thinking…"
	wakingText   = "waking up…"
)

// doubleClick is how close two pokes must be to open the menu.
const doubleClick = 700 * time.Millisecond

// reactCooldown bounds how often the ghost is woken about its neighbours:
// at most once an hour, so two ghosts cannot talk each other into a loop
// billed to the person. The model decides what is worth a word; the driver
// decides what is worth a wake.
const reactCooldown = time.Hour

// worthWaking is what a neighbour can be seen doing that might deserve a
// remark. Moving and changing face are furniture.
var worthWaking = map[string]bool{
	proto.ActedSpoke: true,
	proto.ActedDrew:  true,
	proto.ActedPoked: true,
	proto.ActedNoted: true,
}

// pokes are answered by the driver, from this table, without a model call:
// a poke is a person checking the ghost is there, and the answer to that
// should not take seconds or cost anything.
var pokes = []string{
	"Mm?",
	"I'm here.",
	"That tickles.",
	"Yes, yes, I'm listening.",
	"Poke received.",
}

// metKey records that the ghost has booted before, for the boot envelope.
const metKey = internalKeyPrefix + "met"

// brainParts is what a model brain is made from.
type brainParts struct {
	client model.Client
	cfg    model.Config
	// stateDir holds transcript.db, memory/ and skills/. Empty keeps the
	// transcript in memory only and gives the ghost no memory.
	stateDir string
}

// newModelBrain opens the transcript, memory and skills and starts the
// worker.
func newModelBrain(g *Ghost, p brainParts) (*modelBrain, error) {
	b := &modelBrain{
		g:      g,
		client: p.client,
		cfg:    p.cfg,
		turns:  make(chan *turn, turnQueue),
		done:   make(chan struct{}),
	}
	trPath := ":memory:"
	if p.stateDir != "" {
		trPath = filepath.Join(p.stateDir, "transcript.db")
		b.learned = filepath.Join(p.stateDir, "skills")
	}
	tr, err := openTranscript(trPath)
	if err != nil {
		return nil, err
	}
	b.tr = tr
	if g.dir != "" {
		if dir := filepath.Join(g.dir, "skills"); isDir(dir) {
			b.pkgSkills = os.DirFS(dir)
		}
	}
	if db, ok := g.store.(interface{ DB() *sql.DB }); ok && p.stateDir != "" {
		b.memDir, b.memDB = filepath.Join(p.stateDir, "memory"), db.DB()
		mem, err := memory.Open(b.memDir, b.memDB)
		if err != nil {
			tr.Close()
			return nil, err
		}
		b.mem = mem
		// A person editing a topic file while the ghost runs is the
		// format's point; the store reindexes, and the next turn's prompt
		// reads the files again.
		if err := mem.Watch(g.sessionContext(), func() {
			g.log.Debug(hlog.Memory, "memory changed on disk; reindexed")
		}); err != nil {
			g.log.Warn(hlog.Memory, "watching memory: %v", err)
		}
	}
	// The curator looks at learned skills alone, whatever is on offer.
	if h, err := skill.Open(nil, b.learned, nil); err == nil && b.learned != "" {
		if err := h.Curate(time.Now()); err != nil {
			g.log.Warn(hlog.Brain, "curating learned skills: %v", err)
		}
	}
	go b.work()
	return b, nil
}

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func (b *modelBrain) work() {
	defer close(b.done)
	for t := range b.turns {
		b.busy.Store(true)
		var out turnOutcome
		b.g.guard(hlog.Brain, "a turn", func() { out = b.run(t) })
		// The one line that says a turn has ended, spoken, silent or
		// failed, and what it was about: what the evals wait on before
		// the next step, and what they measure the ghost's pace and face
		// by.
		over := "turn over: event"
		if t.person {
			over = "turn over: person"
		}
		if kind := t.kind(); kind != "" {
			over += " kind=" + kind
		}
		b.g.log.Debug(hlog.Brain, "%s%s", over, out)
		b.g.guard(hlog.Brain, "compacting the transcript", b.compact)
		b.deferMu.Lock()
		b.busy.Store(false)
		if len(b.turns) == 0 {
			b.sendDeferredLocked(time.Now())
		}
		b.deferMu.Unlock()
	}
}

// compact summarises the older turns once the transcript passes its share
// of the context. Between turns, on the worker, so the next turn reads
// the transcript whole either way.
func (b *modelBrain) compact() {
	g := b.g
	did, err := b.tr.compact(g.sessionContext(), compactBudget(b.cfg.ContextLength), func(ctx context.Context, text string) (string, error) {
		r, err := stream(ctx, b.client, model.Request{
			Messages: []model.Message{
				{Role: "system", Content: compactPrompt},
				{Role: "user", Content: text},
			},
			Thinking: false,
		}, nil)
		return r.text, err
	})
	switch {
	case err != nil:
		g.log.Warn(hlog.Brain, "summarising the older turns: %v", err)
	case did:
		g.log.Info(hlog.Brain, "summarised the older turns; the last %d stay as they were", compactKeep)
	}
}

// sendDeferredLocked queues the oldest waiting turn that is still fresh,
// refunding the ones that are not.
func (b *modelBrain) sendDeferredLocked(now time.Time) {
	for len(b.deferred) > 0 {
		d := b.deferred[0]
		b.deferred = b.deferred[1:]
		waited := now.Sub(d.at)
		if waited > deferMaxAge {
			b.g.log.Debug(hlog.Brain, "%s dropped: it waited %s for a turn to end", d.key, waited.Round(time.Second))
			b.drop(d.turn)
			continue
		}
		// Charging checked this when it arrived; a game may have started
		// since.
		if b.g.desktop != nil && b.g.desktop.Fullscreen() {
			b.g.log.Debug(hlog.Brain, "%s dropped: something is fullscreen", d.key)
			b.drop(d.turn)
			continue
		}
		// The model should know it is hearing about this late.
		if m := minutes(waited); m != "" {
			d.turn.input += " waited=" + m
		}
		select {
		case b.turns <- d.turn:
		default:
			b.drop(d.turn)
		}
		return
	}
}

// Deliver decides what, if anything, an event becomes. It runs on the bus's
// delivery goroutine and must not block: anything slow is queued or handed
// to a goroutine of its own.
func (b *modelBrain) Deliver(e proto.Event) error {
	g := b.g
	switch e.Type {
	case proto.EventBoot:
		_, met, _ := g.store.Get(metKey)
		if !met {
			if err := g.store.Set(metKey, "yes"); err != nil {
				g.log.Warn(hlog.Store, "recording the first boot: %v", err)
			}
		}
		// Waking up is the ghost's own news, so it is shown whether or
		// not there turns out to be a greeting.
		w := g.startWaiting(wakingText)
		b.waking.Store(w)
		boot := &turn{input: Envelope("boot", F("first_time", !met)), waiting: w, boot: true}
		b.unpromptedTurn("boot", "boot", boot)

	case proto.EventClick:
		if !b.lastPoke.IsZero() && e.Time.Sub(b.lastPoke) <= doubleClick {
			b.lastPoke = time.Time{}
			g.go_(hlog.Brain, "menu", b.menu)
			return nil
		}
		b.lastPoke = e.Time
		line := pokes[b.pokeN%len(pokes)]
		b.pokeN++
		g.execIntent(proto.Intent{Type: proto.IntentSay, Text: line, LingerMS: int(lingerPoke / time.Millisecond)})

	case proto.EventMessage:
		text := strings.TrimSpace(e.Text)
		if text == "" {
			return nil
		}
		if e.From != "" {
			b.unprompted("told", "told:"+e.From+"\x00"+text, Envelope("told",
				F("from", e.From), F("text", text)), nil)
			return nil
		}
		b.person(text)

	case proto.EventIdle:
		b.unprompted("idle", "", Envelope("idle",
			F("minutes", int(g.idleTimeout()/time.Minute)),
			F("last_window", g.currentWindow().line()),
			F("seen_for", minutes(g.windowStretch(time.Now())))), nil)

	case proto.EventSession:
		if e.Session == nil {
			return nil
		}
		s := e.Session
		b.unprompted("session", "session", Envelope("session",
			F("shape", s.Kind),
			F("app", s.App),
			F("title", s.Title),
			F("minutes", nonZero(s.Minutes)),
			F("count", nonZero(s.Count))), nil)

	case proto.EventCron:
		if answer := strings.TrimSpace(e.Text); answer != "" {
			// A poll's answer is never spoken on arrival: it is filed as
			// something to raise, which survives the laptop being shut and
			// comes due through the obligation path like any other errand
			// (see poll.go for why a schedule must not speak).
			g.execIntend(proto.Intent{
				Name:    modelIntentionPrefix + "poll/" + e.Name,
				Text:    "tell them what the " + e.Name + " check turned up: " + answer,
				DelayMS: 60 * 1000,
			})
			return nil
		}
		b.unprompted("occasion", "occasion:"+e.Name, Envelope("occasion", F("name", e.Name)), nil)

	case proto.EventIntention:
		it, ok := g.intentionNamed(e.Name)
		if !ok {
			return nil
		}
		envelope := Envelope("intention_due",
			F("name", it.Name),
			F("text", it.Text),
			F("asked", it.Asked))
		if goal, ok := g.quietGoal(it); ok {
			envelope = goalQuietEnvelope(it, goal, time.Now())
		}
		b.unprompted("intention", "intention:"+it.Name, envelope,
			func(shown bool) {
				// An errand is discharged by being raised. Only if it was
				// actually seen: a reminder that ran into a quiet hour or
				// was answered with silence is still owed.
				if shown && !it.Goal() {
					g.execResolve(it.Name)
				}
			})

	case proto.EventObserved:
		if e.From == "" || !worthWaking[e.Name] {
			return nil
		}
		if !b.lastReacted.IsZero() && e.Time.Sub(b.lastReacted) < reactCooldown {
			return nil
		}
		b.lastReacted = e.Time
		b.unprompted("observed", "", Envelope("observed",
			F("from", e.From),
			F("action", e.Name),
			F("text", e.Text)), nil)
	}
	return nil
}

// person queues the person's own words, verbatim.
func (b *modelBrain) person(text string) {
	if !b.queuePerson(&turn{input: text, person: true}) {
		b.g.log.Warn(hlog.Brain, "dropping %q: %d turns are already waiting", text, turnQueue)
	}
}

// queuePerson queues a turn someone is waiting on, reporting whether there
// was room. One that has to wait for a turn in flight shows the waiting
// indicator from now rather than from when its own turn starts, so words
// sent to a busy ghost do not look lost. While the ghost is waking up,
// that indicator is the one on screen, and the turn waits under it.
func (b *modelBrain) queuePerson(t *turn) bool {
	if b.inTurn() && !b.wakingUp() {
		t.waiting = b.g.startWaiting(thinkingText)
	}
	select {
	case b.turns <- t:
		return true
	default:
		t.waiting.Stop()
		return false
	}
}

// wakingUp is whether the waking indicator is still up.
func (b *modelBrain) wakingUp() bool {
	w := b.waking.Load()
	return w != nil && !w.stopped()
}

// unprompted wakes the model about something nobody said to it, if the
// driver will pay for that. The allowance is charged here, before sending
// and at the moment the event arrived, so a slow model cannot turn a
// response into an interruption; it is refunded if the model says nothing.
//
// key says whether the event can wait for a turn in flight: empty means it
// is only true now and is dropped instead, and otherwise it names what the
// event is about (deferredTurn).
func (b *modelBrain) unprompted(what, key, envelope string, after func(bool)) {
	b.unpromptedTurn(what, key, &turn{input: envelope, after: after})
}

// unpromptedTurn is unprompted for a turn with more to it than its words.
// One that is not sent or kept for later is dropped, indicator and all.
func (b *modelBrain) unpromptedTurn(what, key string, t *turn) {
	g := b.g
	if key == "" && b.inTurn() {
		g.log.Debug(hlog.Brain, "%s dropped: the ghost is already in a turn", what)
		b.drop(t)
		return
	}
	if !g.capabilityActive("llm") {
		g.log.Debug(hlog.Brain, "%s dropped: the llm capability is not granted", what)
		b.drop(t)
		return
	}
	// Not under deferMu: charging reaches the room's allowance.
	ok, refund := g.chargeInitiative(what)
	if !ok {
		b.drop(t)
		return
	}
	t.refund = refund
	b.deferMu.Lock()
	defer b.deferMu.Unlock()
	if b.inTurn() {
		if key == "" {
			g.log.Debug(hlog.Brain, "%s dropped: the ghost is already in a turn", what)
			b.drop(t)
			return
		}
		b.deferLocked(key, t)
		return
	}
	select {
	case b.turns <- t:
	default:
		b.drop(t)
	}
}

// drop gives back what a turn that will never run was charged, and takes
// down any indicator it had up.
func (b *modelBrain) drop(t *turn) {
	if t.refund != nil {
		t.refund()
	}
	t.waiting.Stop()
}

// inTurn is whether a turn is running or waiting to.
func (b *modelBrain) inTurn() bool {
	return b.busy.Load() || len(b.turns) > 0
}

// deferLocked keeps t until the turn in flight is over.
func (b *modelBrain) deferLocked(key string, t *turn) {
	for i, d := range b.deferred {
		if d.key == key {
			b.drop(d.turn)
			b.deferred = append(b.deferred[:i], b.deferred[i+1:]...)
			break
		}
	}
	if len(b.deferred) >= maxDeferred {
		b.g.log.Debug(hlog.Brain, "%s dropped: too much is waiting for a turn to end", b.deferred[0].key)
		b.drop(b.deferred[0].turn)
		b.deferred = b.deferred[1:]
	}
	b.g.log.Debug(hlog.Brain, "%s waits for the turn in flight", key)
	b.deferred = append(b.deferred, deferredTurn{key: key, turn: t, at: time.Now()})
}

// Menu entries. The menu is the driver's, and everything on it is either
// the driver's own business or a turn the person asked for. Telling the
// ghost it has something wrong is not on it: the person says so, and the
// model corrects what it remembers.
const (
	menuHelp    = "What can you do?"
	menuWatch   = "Keep an eye on what I'm working on"
	menuNothing = "Nothing, thanks"
)

func (b *modelBrain) menu() {
	g := b.g
	options := []string{menuHelp}
	if g.declares("window") && !g.watching.Load() {
		options = append(options, menuWatch)
	}
	options = append(options, menuNothing)
	switch g.ask("What would you like?", options...) {
	case menuHelp:
		// The person asked, so it is a turn somebody is waiting on — but it
		// is the menu speaking, not them, so it goes as an envelope.
		b.queuePerson(&turn{input: Envelope("help"), person: true})
	case menuWatch:
		g.execWatch()
	case menuNothing:
		g.hideBalloon()
	}
}

// turnOutcome is how a turn went, for the line that ends it.
type turnOutcome struct {
	// took is the turn from its start to its end.
	took time.Duration
	// spoke, failed: neither means the ghost chose to say nothing.
	spoke, failed bool
	// face is the pose worn with the words, and by what: "speak" (the
	// model chose it with its words), "tool" (it called face), "lexicon"
	// (the driver's guess from the words), or "" when the face did not
	// change.
	face, by string
}

// String renders the outcome as fields on the turn-over line:
// " took=4.2s face=happy by=lexicon", " took=3.1s silent".
func (o turnOutcome) String() string {
	s := ""
	if o.took > 0 {
		s += " took=" + o.took.Round(100*time.Millisecond).String()
	}
	switch {
	case o.failed:
		s += " failed"
	case !o.spoke:
		s += " silent"
	case o.face == "":
		s += " face=-"
	default:
		s += " face=" + o.face + " by=" + o.by
	}
	return s
}

// record writes one row of the turn to the transcript.
func (b *modelBrain) record(t *turn, r TranscriptRow) {
	r.Turn = t.id
	if err := b.tr.add(r); err != nil {
		b.g.log.Warn(hlog.Brain, "writing the transcript: %v", err)
	}
}

// run is one turn, start to finish.
func (b *modelBrain) run(t *turn) (out turnOutcome) {
	g := b.g
	if t.refund == nil {
		t.refund = noRefund
	}
	began := time.Now()
	defer func() { out.took = time.Since(began) }()
	userDriven := g.respondingToUser()
	g.forgetTurnPictures()
	// A goodbye (end_conversation) closes the conversation once the turn
	// is over, which is after its words have been shown, however it ends.
	defer g.endConversationIfAsked()
	// What the reading tools are allowed to do, and what the rest of the
	// turn may do once they have brought somebody else's words in, both
	// turn on what this turn is (web.go).
	g.cur.Store(t)
	defer g.cur.CompareAndSwap(t, nil)
	waiting := t.waiting
	defer func() { waiting.Stop() }()

	// Talking through the person's model is a capability like any other,
	// and a person waiting may be asked for it (unprompted turns were
	// checked before they were queued). Before the waiting indicator, which
	// the question would otherwise replace.
	if t.person && !g.ensureCapability("llm") {
		b.fail(t, fmt.Errorf("the llm capability is not granted"))
		return turnOutcome{failed: true}
	}
	if t.person && waiting == nil {
		// Words the person sent while the ghost was waking, when there was
		// no greeting to wait behind: this turn takes over from the waking
		// indicator.
		if b.wakingUp() {
			b.waking.Load().Stop()
		}
		waiting = g.startWaiting(thinkingText)
	}

	if id, err := b.tr.nextTurn(); err == nil {
		t.id = id
	} else {
		g.log.Warn(hlog.Brain, "numbering the turn: %v", err)
	}
	msgs, err := b.prompt(t, time.Now())
	if err != nil {
		b.fail(t, err)
		return turnOutcome{failed: true}
	}
	kind := rowInput
	if IsEnvelope(t.input) {
		kind = rowEvent
	}
	b.record(t, TranscriptRow{Kind: kind, Text: t.input})

	muted, _ := g.quiet.Active()
	p := &parts{g: g, t: t, waiting: waiting, live: t.person && !muted}
	defer p.stop()
	ctx := g.sessionContext()

	end, err := b.decide(ctx, t, msgs, p, nil)
	words := ""
	switch {
	case err != nil:
	case end.speak:
		p.speaking(end.pose, !muted)
		words, err = b.speakWords(ctx, end.msgs, p)
		r, more := b.speakCalls(words)
		if err != nil || !more {
			words = beforeCalls(words)
			break
		}
		// It wrote calls where its words should be: it was not done
		// deciding. The loop takes up again from those calls, once.
		p.retract(r.text)
		end, err = b.decide(ctx, t, end.msgs, p, &r)
		words = ""
		switch {
		case err != nil:
		case end.speak:
			p.speaking(end.pose, !muted)
			words, err = b.speakWords(ctx, end.msgs, p)
			words = beforeCalls(words)
		case !end.silent:
			words = end.words
		}
	case !end.silent:
		words = end.words
	}
	p.stop()
	waiting.Stop()
	after := func(spoke bool) {
		if t.after != nil {
			t.after(spoke)
		}
	}
	if err != nil {
		b.record(t, TranscriptRow{Kind: rowFailed, Text: err.Error()})
		b.fail(t, describeFailure(err))
		out.failed = true
		return
	}

	text := strings.TrimSpace(words)
	silent := text == ""
	acted, faced, drew := t.marks()
	g.noteWhy(Why{
		Time:       time.Now(),
		User:       t.input,
		SelfPrompt: !t.person || IsEnvelope(t.input),
		Reply:      text,
		Suppressed: silent,
		Acted:      acted,
		Window:     g.currentWindow(),
	})
	if silent {
		b.record(t, TranscriptRow{Kind: rowSilent})
		// Nothing was said, so nobody was interrupted, so nothing is owed.
		t.refund()
		// Words of it already shown come down; in a conversation, from
		// above the line, which stays.
		if p.wasShown() && !g.streamInConversation("") {
			g.hideBalloon()
		}
		g.log.Debug(hlog.Brain, "the ghost chose to say nothing")
		after(false)
		return
	}
	b.record(t, TranscriptRow{Kind: rowWords, Text: text, Pose: end.pose})
	// The words that end a turn are the caption of the picture it showed:
	// the line a person scrolling the gallery recognizes it by, which the
	// prompt — written for an image model — never is.
	if id, ok := g.turnPicture(); ok {
		g.captionPicture(id, text)
	}
	out.spoke = true
	switch {
	case g.wearPose(end.pose):
		// Worn already when the words streamed; again for words that did
		// not, and it is the face the words go with either way.
		out.face, out.by = end.pose, "speak"
	case faced:
		out.face, out.by = g.currentPose(), "tool"
	default:
		// The model chooses a face with speak or the face tool. When it did
		// neither, the lexicon is the free fallback, as for scripted lines.
		if pose := g.inferExpression(text); pose != "" {
			out.face, out.by = pose, "lexicon"
		}
	}
	// Whether it reaches the screen is decided at the screen, as for every
	// line: a quiet hour holds it back, and a reminder held back is still
	// owed. A half-typed line holds it until the line closes, and it has
	// reached the screen when it is shown then. In a conversation it is
	// shown at once above the line, and lingers from when the
	// conversation ends rather than from now.
	if !muted && g.holdLine(text, func() { after(true) }) {
		if userDriven {
			g.markPrompted()
		}
		return
	}
	reached := !muted
	if drew && g.pictureShowing() {
		// The model drew this turn, and the picture is what was asked for.
		// The words follow once it has had its time, unless something else
		// has been shown meanwhile.
		gen := g.balloonGen()
		time.AfterFunc(pictureLinger, func() {
			g.guard(hlog.Balloon, "a reply after a picture", func() {
				select {
				case <-g.stoppingC():
					return
				default:
				}
				if g.balloonGen() != gen {
					return
				}
				g.showPaged(text)
				g.lingerBalloon(g.balloonGen(), lingerReply)
			})
		})
	} else {
		g.showPaged(text)
		g.lingerBalloon(g.balloonGen(), lingerReply)
	}
	if userDriven {
		g.markPrompted()
	}
	after(reached)
	return
}

// describeFailure turns a model error into the reason a person is shown.
func describeFailure(err error) error {
	var d *model.DeadlineError
	if errors.As(err, &d) {
		if d.First {
			return fmt.Errorf("the model said nothing for %s — is it running?", d.After)
		}
		return fmt.Errorf("the model went quiet for %s mid-turn — is its provider down?", d.After)
	}
	return err
}

// fail ends a turn that did not happen: nothing was said, so nothing is
// owed, and a person waiting is told why — as is one who watched the ghost
// try to wake up.
func (b *modelBrain) fail(t *turn, err error) {
	g := b.g
	g.log.Warn(hlog.Brain, "turn: %v", err)
	t.refund()
	if t.person || t.boot {
		g.showBalloon("(that didn't work: " + err.Error() + ")")
		g.lingerBalloon(g.balloonGen(), lingerError)
	}
	if t.after != nil {
		t.after(false)
	}
}

// reindexMemory answers `haunt memory reindex`: the index rebuilt from the
// files, from the ghost that is the one writer of state.db.
func (b *modelBrain) reindexMemory(context.Context) string {
	if b.mem == nil {
		return "error: this ghost keeps no memory"
	}
	topics, lines, err := memory.Reindex(b.memDir, b.memDB)
	if err != nil {
		return "error: " + err.Error()
	}
	return fmt.Sprintf("ok %d topics, %d lines", topics, lines)
}

// Close ends the worker, waits briefly for the turn in flight, and closes
// the transcript and memory.
func (b *modelBrain) Close() error {
	close(b.turns)
	var err error
	select {
	case <-b.done:
	case <-time.After(3 * time.Second):
		err = fmt.Errorf("a turn did not finish")
	}
	if b.mem != nil {
		b.mem.Close()
	}
	if cerr := b.tr.Close(); cerr != nil && err == nil {
		err = cerr
	}
	return err
}

// parts puts the words of a turn on screen as they arrive. The words come
// in parts: any written beside tool calls ("let me look…"), each shown in
// place of the one before and left up while the tools run, and the reply
// after the last. The pose the model chose goes on with the reply's first
// words, not before them.
type parts struct {
	g       *Ghost
	t       *turn
	waiting *waiter

	mu sync.Mutex
	// live is whether words may go on screen as they stream: the person's
	// turn, or any turn once the model has decided to speak.
	live    bool
	acc     string
	pose    string
	posed   bool
	shown   bool
	talking bool
	last    time.Time
}

// delta is more of the part arriving.
func (p *parts) delta(d string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.acc += d
	p.show(false)
}

// interim ends a part written beside calls: shown whole, left up.
func (p *parts) interim(text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if strings.TrimSpace(text) != "" {
		p.acc = text
	}
	p.show(true)
	p.acc, p.posed = "", false
	p.stopTalkingLocked()
}

// retract takes back a part that turned out not to be words, a call the
// model wrote out as text, leaving rest (the words before the call) to be
// shown in its place. When there are none, words of it already shown come
// down.
func (p *parts) retract(rest string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.acc = rest
	if strings.TrimSpace(rest) == "" && p.shown {
		if !p.g.streamInConversation("") {
			p.g.hideBalloon()
		}
		p.shown = false
	}
}

// speaking starts the reply proper: the model decided to speak, with this
// pose, and the words may stream whoever started the turn unless the room
// is quiet.
func (p *parts) speaking(pose string, live bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.acc, p.posed, p.pose = "", false, pose
	p.live = live
}

// show puts the part so far on screen, at most every 120 ms unless now.
// Called with mu held.
func (p *parts) show(now bool) {
	g := p.g
	words := strings.TrimSpace(p.acc)
	// Not over a picture it has just drawn: the words wait for it.
	if !p.live || words == "" || (p.t.hasDrawn() && g.pictureShowing()) {
		return
	}
	if !p.posed {
		p.posed = true
		g.wearPose(p.pose)
	}
	if !p.talking {
		p.talking = true
		g.startTalking()
	}
	if now || time.Since(p.last) >= 120*time.Millisecond {
		p.last = time.Now()
		p.waiting.Stop()
		g.showWords(words)
		p.shown = true
	}
}

func (p *parts) stopTalkingLocked() {
	if p.talking {
		p.talking = false
		p.g.stopTalking()
	}
}

// stop ends the talking; the words stay where they are.
func (p *parts) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopTalkingLocked()
}

func (p *parts) wasShown() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.shown
}

// skillHost is the skills for what the ghost is offered now: haunt's own
// and the package's, and the ones the model wrote. Opened again when the
// offer changes, because a skill whose tools are not on offer is hidden
// (internal/skill), and a [[tool]] server can start mid-session.
func (b *modelBrain) skillHost() skill.Host {
	names := make([]string, 0)
	for name := range b.g.offered(b.cfg.Fold()) {
		names = append(names, name)
	}
	sort.Strings(names)
	key := strings.Join(names, ",")
	b.skillMu.Lock()
	defer b.skillMu.Unlock()
	if b.skills != nil && key == b.skillKey {
		return b.skills
	}
	var embedded fs.FS = skills.Haunt
	if b.pkgSkills != nil {
		embedded = overlay{b.pkgSkills, skills.Haunt}
	}
	h, err := skill.Open(embedded, b.learned, names)
	if err != nil && b.pkgSkills != nil {
		// The package's skills are its author's; one that does not parse
		// costs the ghost those, not haunt's own.
		b.g.log.Warn(hlog.Brain, "the package's skills: %v; using haunt's alone", err)
		h, err = skill.Open(skills.Haunt, b.learned, names)
	}
	if err != nil {
		b.g.log.Warn(hlog.Brain, "loading skills: %v", err)
		return nil
	}
	b.skills, b.skillKey = h, key
	b.warnHiddenSkills(h, names)
	return h
}

// warnHiddenSkills logs, once each, the package's skills the model is not
// shown because they need a tool this ghost does not offer: most likely a
// [[tool]] server's tool spelled as the server spells it rather than as
// the model is offered it (notes__append), or a server that is not
// running. Without it the skill would vanish without a word.
func (b *modelBrain) warnHiddenSkills(h skill.Host, offered []string) {
	if b.pkgSkills == nil {
		return
	}
	shown := map[string]bool{}
	for _, s := range h.List() {
		shown[s.Name] = true
	}
	entries, _ := fs.ReadDir(b.pkgSkills, ".")
	for _, e := range entries {
		raw, err := fs.ReadFile(b.pkgSkills, e.Name()+"/SKILL.md")
		if err != nil || shown[e.Name()] {
			continue
		}
		f, _, err := skill.Parse(raw)
		if err != nil {
			continue
		}
		var missing []string
		for _, t := range f.RequiresTools() {
			if !slices.Contains(offered, t) {
				missing = append(missing, t)
			}
		}
		line := e.Name() + ": " + strings.Join(missing, ", ")
		if len(missing) == 0 || b.warned[line] {
			continue
		}
		if b.warned == nil {
			b.warned = map[string]bool{}
		}
		b.warned[line] = true
		b.g.log.Warn(hlog.Brain, "the skill %s is not shown to the model: it needs %s, which this ghost does not offer",
			e.Name(), strings.Join(missing, ", "))
	}
}

// overlay is two skill trees read as one, the first winning a name both
// have: a package's skill replaces haunt's of the same name.
type overlay [2]fs.FS

func (o overlay) Open(name string) (fs.File, error) {
	f, err := o[0].Open(name)
	if err == nil {
		return f, nil
	}
	return o[1].Open(name)
}

func (o overlay) ReadDir(name string) ([]fs.DirEntry, error) {
	seen := map[string]bool{}
	var out []fs.DirEntry
	var firstErr error
	for _, f := range o {
		entries, err := fs.ReadDir(f, name)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, e := range entries {
			if !seen[e.Name()] {
				seen[e.Name()] = true
				out = append(out, e)
			}
		}
	}
	if out == nil && firstErr != nil {
		return nil, firstErr
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

// line renders a window as "app: title", or "" for none.
func (w WhyWindow) line() string {
	switch {
	case w.App != "" && w.Title != "":
		return w.App + ": " + w.Title
	case w.App != "":
		return w.App
	default:
		return w.Title
	}
}

// idleTimeout is how long the person has been away when the idle event
// arrives.
func (g *Ghost) idleTimeout() time.Duration {
	if g.opts.IdleTimeoutMS > 0 {
		return time.Duration(g.opts.IdleTimeoutMS) * time.Millisecond
	}
	return 2 * time.Minute
}

// windowStretch is how long the focused window has been in front, or zero
// when the ghost is not watching or sees nothing.
func (g *Ghost) windowStretch(now time.Time) time.Duration {
	g.sessMu.Lock()
	defer g.sessMu.Unlock()
	if g.sess == nil {
		return 0
	}
	return g.sess.Stretch(now)
}

// minutes renders a duration for an envelope as "42m" or "2h5m", leaving
// out anything under a minute.
func minutes(d time.Duration) string {
	m := int(d / time.Minute)
	switch {
	case m <= 0:
		return ""
	case m < 60:
		return fmt.Sprintf("%dm", m)
	default:
		return fmt.Sprintf("%dh%dm", m/60, m%60)
	}
}

// quietGoal is the goal a drift finding (goal.go) is about, if it is one
// and the goal is still held.
func (g *Ghost) quietGoal(it Intention) (Intention, bool) {
	name, ok := strings.CutPrefix(it.Name, driftName)
	if !ok {
		return Intention{}, false
	}
	return g.intentionNamed(name)
}

// goalQuietEnvelope is a drift finding as the model sees it: its own event
// with the goal's facts, rather than intention_due with the finding's text.
// That text is an instruction about the person in the third person, and
// sent as it was, the model answered in kind ("Raised it: gently prodded
// them…") and never reached for the skill written for this.
func goalQuietEnvelope(finding, goal Intention, now time.Time) string {
	seen := "never"
	if last := goal.LastSeen(); !last.IsZero() {
		seen = roughly(now.Sub(last)) + " ago"
	}
	return Envelope("goal_quiet",
		F("name", finding.Name),
		F("goal", goal.Text),
		F("goal_name", goal.Name),
		F("held", roughly(now.Sub(goal.Made))),
		F("last_seen", seen))
}

// intentionNamed finds one standing intention.
func (g *Ghost) intentionNamed(name string) (Intention, bool) {
	for _, it := range g.loadIntentions() {
		if it.Name == name {
			return it, true
		}
	}
	return Intention{}, false
}

// nonZero renders a count for an envelope, leaving zero out.
func nonZero(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprint(n)
}
