package ghost

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aaronjauregui/haunt/internal/brain"
	"github.com/aaronjauregui/haunt/internal/hermes"
	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/proto"
	"github.com/aaronjauregui/haunt/skills"
	hermesskills "github.com/aaronjauregui/haunt/third_party/hermes-skills"
)

// The hermes brain is what a ghost with no [brain] section runs on: its
// judgment is hermes-agent's, and everything here is the driver deciding
// what reaches it and putting what comes back on screen.
//
// It is a brain.Runner hosted in the driver, but it does not speak
// intents. What the model wants done arrives as tool calls over the control
// socket (hermestools.go) and is performed there, under the same gates as
// everything else; what the model says arrives as the turn's reply and is
// shown here. The division of labour is the one
// docs/dev/hermes.md draws: the driver owns effects — who pays for a wake,
// whether the balloon may show, what the person sees — and the model owns
// what to make of an event and whether to say anything at all.
//
// One turn runs at a time. The person's words queue behind a turn in
// flight. Something unprompted that arrives meanwhile waits too, if it is
// still worth raising once the turn is over (see deferrable); what is only
// true in the moment — the person going idle, a neighbour's remark — is
// dropped, because by then it is stale and the ghost has just been talking.
type hermesBrain struct {
	g      *Ghost
	turner hermes.Turner
	// stop ends whatever the brain started to be reachable — the gateway
	// process, in practice. Nil when there is nothing to stop.
	stop func() error

	turns chan hermesTurn
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

	// waking is the indicator a hermes ghost shows from the moment it is
	// on screen until it has said hello: the gateway's start and the boot
	// turn are half a minute on a slow model, and a ghost that sits there
	// silent through them reads as broken.
	waking atomic.Pointer[waiter]
}

var _ brain.Runner = (*hermesBrain)(nil)

// hermesTurn is one thing to send.
type hermesTurn struct {
	input string
	// person says someone is waiting on this turn: the reply streams into
	// the balloon as it arrives, under a waiting indicator. An unprompted
	// turn is held back until it is finished, because most of them end in
	// NO_REPLY and nobody should see the ghost start to say nothing.
	person bool
	// refund puts back what was spent to wake the ghost, when it turns out
	// to have had nothing to say.
	refund func()
	// after runs once the turn is over, with whether anything reached the
	// screen.
	after func(shown bool)
	// waiting is the indicator already up for this turn: a person's turn
	// queued behind something, started when it was queued so that words
	// sent to a ghost still starting, or busy, do not look lost; or the
	// boot turn's, which is the waking indicator.
	waiting *waiter
	// boot says this is the greeting, whose failure the person is told
	// about: they were shown the ghost waking up.
	boot bool
}

// deferredTurn is an unprompted turn waiting for the one in flight.
type deferredTurn struct {
	// key names what it is about; a newer turn with the same key replaces
	// it, since only the latest word on one subject is worth sending.
	key  string
	turn hermesTurn
	at   time.Time
}

// Waiting unprompted turns: how many, and for how long. Past the age, the
// moment has gone; an errand is still carried on every turn's context
// (turnContext), so a reminder that expires here is not lost.
const (
	maxDeferred = 4
	deferMaxAge = 10 * time.Minute
)

// hermesQueue bounds the person's words waiting behind a turn. Past it,
// somebody is typing faster than any model answers, and the oldest-first
// order would be answering things they have stopped caring about.
const hermesQueue = 8

// Lingers: long enough to read.
const (
	lingerPoke  = 4 * time.Second
	lingerReply = 12 * time.Second
	lingerError = 8 * time.Second
)

// thinkingText is what the waiting indicator says for a turn someone is
// waiting on, and wakingText what it says while the gateway is still
// starting.
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

// newHermesBrain starts the worker. turner carries the turns; stop, if set,
// is called on Close after the last turn.
func newHermesBrain(g *Ghost, turner hermes.Turner, stop func() error) *hermesBrain {
	b := &hermesBrain{
		g:      g,
		turner: turner,
		stop:   stop,
		turns:  make(chan hermesTurn, hermesQueue),
		done:   make(chan struct{}),
	}
	go b.work()
	return b
}

func (b *hermesBrain) work() {
	defer close(b.done)
	for t := range b.turns {
		b.busy.Store(true)
		b.g.guard(hlog.Brain, "a hermes turn", func() { b.run(t) })
		// The one line that says a turn has ended, spoken, silent or
		// failed, and what it was about: what the evals wait on before
		// the next step.
		over := "turn over: event"
		if t.person {
			over = "turn over: person"
		}
		if kind := hermes.EnvelopeKind(t.input); kind != "" {
			over += " kind=" + kind
		}
		b.g.log.Debug(hlog.Brain, "%s", over)
		b.deferMu.Lock()
		b.busy.Store(false)
		if len(b.turns) == 0 {
			b.sendDeferredLocked(time.Now())
		}
		b.deferMu.Unlock()
	}
}

// sendDeferredLocked queues the oldest waiting turn that is still fresh,
// refunding the ones that are not.
func (b *hermesBrain) sendDeferredLocked(now time.Time) {
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
func (b *hermesBrain) Deliver(e proto.Event) error {
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
		// not there turns out to be a greeting; with none, it is gone
		// once the gateway is up and the ghost is ready.
		w := g.startWaiting(wakingText)
		b.waking.Store(w)
		boot := hermesTurn{input: hermes.Envelope("boot", hermes.F("first_time", !met)), waiting: w, boot: true}
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
			b.unprompted("told", "told:"+e.From+"\x00"+text, hermes.Envelope("told",
				hermes.F("from", e.From), hermes.F("text", text)), nil)
			return nil
		}
		b.person(text)

	case proto.EventIdle:
		b.unprompted("idle", "", hermes.Envelope("idle",
			hermes.F("minutes", int(g.idleTimeout()/time.Minute)),
			hermes.F("last_window", g.currentWindow().line()),
			hermes.F("seen_for", minutes(g.windowStretch(time.Now())))), nil)

	case proto.EventSession:
		if e.Session == nil {
			return nil
		}
		s := e.Session
		b.unprompted("session", "session", hermes.Envelope("session",
			hermes.F("shape", s.Kind),
			hermes.F("app", s.App),
			hermes.F("title", s.Title),
			hermes.F("minutes", nonZero(s.Minutes)),
			hermes.F("count", nonZero(s.Count))), nil)

	case proto.EventCron:
		if answer := strings.TrimSpace(e.Text); answer != "" {
			// A poll's answer is never spoken on arrival: it is filed as
			// something to raise, which survives the laptop being shut and
			// comes due through the obligation path like any other errand
			// (see poll.go for why a schedule must not speak).
			g.execIntend(proto.Intent{
				Name:    "hermes/poll/" + e.Name,
				Text:    "tell them what the " + e.Name + " check turned up: " + answer,
				DelayMS: 60 * 1000,
			})
			return nil
		}
		b.unprompted("occasion", "occasion:"+e.Name, hermes.Envelope("occasion", hermes.F("name", e.Name)), nil)

	case proto.EventIntention:
		it, ok := g.intentionNamed(e.Name)
		if !ok {
			return nil
		}
		envelope := hermes.Envelope("intention_due",
			hermes.F("name", it.Name),
			hermes.F("text", it.Text),
			hermes.F("asked", it.Asked))
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
		b.unprompted("observed", "", hermes.Envelope("observed",
			hermes.F("from", e.From),
			hermes.F("action", e.Name),
			hermes.F("text", e.Text)), nil)
	}
	return nil
}

// person queues the person's own words, verbatim.
func (b *hermesBrain) person(text string) {
	if !b.queuePerson(hermesTurn{input: text, person: true}) {
		b.g.log.Warn(hlog.Brain, "dropping %q: %d turns are already waiting", text, hermesQueue)
	}
}

// queuePerson queues a turn someone is waiting on, reporting whether there
// was room. One that has to wait — for the gateway to come up, or for a
// turn in flight — shows the waiting indicator from now rather than from
// when its own turn starts: at a first start that is the container, then
// the boot greeting, and a ghost silent through all of it looks as though
// it never heard.
//
// While the ghost is waking up, that indicator is the one on screen, and
// the turn waits under it.
func (b *hermesBrain) queuePerson(t hermesTurn) bool {
	if (b.inTurn() || !b.up()) && !b.wakingUp() {
		t.waiting = b.g.startWaitingFor(b.waitingText)
	}
	select {
	case b.turns <- t:
		return true
	default:
		t.waiting.Stop()
		return false
	}
}

// up is whether the gateway has finished starting. A turner that is not
// started late is up from the beginning.
func (b *hermesBrain) up() bool {
	if u, ok := b.turner.(interface{ Up() bool }); ok {
		return u.Up()
	}
	return true
}

// whenUp calls f once the gateway has finished starting, or at once for a
// turner that is up from the beginning.
func (b *hermesBrain) whenUp(f func()) {
	r, ok := b.turner.(interface{ Ready(context.Context) error })
	if !ok || b.up() {
		f()
		return
	}
	b.g.go_(hlog.Brain, "waiting for the gateway", func() {
		r.Ready(b.g.sessionContext())
		f()
	})
}

// wakingUp is whether the waking indicator is still up.
func (b *hermesBrain) wakingUp() bool {
	w := b.waking.Load()
	return w != nil && !w.stopped()
}

// waitingText is what a person waiting on the ghost is told.
func (b *hermesBrain) waitingText() string {
	if !b.up() {
		return wakingText
	}
	return thinkingText
}

// unprompted wakes the model about something nobody said to it, if the
// driver will pay for that. The allowance is charged here, before sending
// and at the moment the event arrived, so a slow model cannot turn a
// response into an interruption; it is refunded if the model says nothing.
//
// key says whether the event can wait for a turn in flight: empty means it
// is only true now and is dropped instead, and otherwise it names what the
// event is about (deferredTurn).
func (b *hermesBrain) unprompted(what, key, envelope string, after func(bool)) {
	b.unpromptedTurn(what, key, hermesTurn{input: envelope, after: after})
}

// unpromptedTurn is unprompted for a turn with more to it than its words.
// One that is not sent or kept for later is dropped, indicator and all.
func (b *hermesBrain) unpromptedTurn(what, key string, t hermesTurn) {
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
// down any indicator it had up: at once, or for the greeting, once the
// ghost is ready, which is what the waking indicator is waiting for.
func (b *hermesBrain) drop(t hermesTurn) {
	if t.refund != nil {
		t.refund()
	}
	if t.boot {
		b.whenUp(t.waiting.Stop)
		return
	}
	t.waiting.Stop()
}

// inTurn is whether a turn is running or waiting to.
func (b *hermesBrain) inTurn() bool {
	return b.busy.Load() || len(b.turns) > 0
}

// deferLocked keeps t until the turn in flight is over.
func (b *hermesBrain) deferLocked(key string, t hermesTurn) {
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
// model edits its memory.
const (
	menuHelp    = "What can you do?"
	menuWatch   = "Keep an eye on what I'm working on"
	menuNothing = "Nothing, thanks"
)

func (b *hermesBrain) menu() {
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
		b.queuePerson(hermesTurn{input: hermes.Envelope("help"), person: true})
	case menuWatch:
		g.execWatch()
	case menuNothing:
		g.hideBalloon()
	}
}

// run is one turn, start to finish.
func (b *hermesBrain) run(t hermesTurn) {
	g := b.g
	if t.refund == nil {
		t.refund = noRefund
	}
	userDriven := g.respondingToUser()
	// A deferred closure, not a deferred call: waiting may be started
	// below, after the capability check.
	waiting := t.waiting
	defer func() { waiting.Stop() }()

	// Talking through the person's model is a capability like any other,
	// and a person waiting may be asked for it (unprompted turns were
	// checked before they were queued). Before the waiting indicator, which
	// the question would otherwise replace.
	if t.person && !g.ensureCapability("llm") {
		b.fail(t, fmt.Errorf("the llm capability is not granted"))
		return
	}

	if t.person && waiting == nil {
		// Words the person sent while the ghost was waking, when there
		// was no greeting to wait behind: this turn takes over from the
		// waking indicator.
		if b.wakingUp() {
			b.waking.Load().Stop()
		}
		waiting = g.startWaitingFor(b.waitingText)
	}

	// A gateway still starting is not a gateway that has gone quiet, so the
	// wait for it is not charged to the first-token deadline below.
	if r, ok := b.turner.(interface{ Ready(context.Context) error }); ok {
		if err := r.Ready(g.sessionContext()); err != nil {
			b.fail(t, fmt.Errorf("hermes is not available: %w", err))
			return
		}
	}

	// The deadline is on the first sign of life — a word or a tool call —
	// rather than on the whole turn: a tool that waits on a person's answer
	// is working, and a gateway that has said nothing at all is not. The
	// indicator is not a sign of life but a promise of words, so it stays
	// until there are some on screen: a turn that wears a face and then
	// thinks again is still a turn somebody is waiting on.
	ctx, cancel := context.WithCancel(g.sessionContext())
	defer cancel()
	var timedOut atomic.Bool
	deadline := time.AfterFunc(g.firstTokenTimeout(), func() {
		timedOut.Store(true)
		cancel()
	})
	defer deadline.Stop()
	var alive sync.Once
	wake := func() {
		alive.Do(func() { deadline.Stop() })
	}

	var (
		mu      sync.Mutex
		acc     string
		shown   bool
		talking bool
		faced   bool
		drew    bool
		last    time.Time
	)
	onDelta := func(delta string) {
		wake()
		mu.Lock()
		defer mu.Unlock()
		acc += delta
		// Not over a picture it has just drawn: the words wait for it.
		if !t.person || hermes.MaybeSilent(acc) || (drew && g.pictureShowing()) {
			return
		}
		if !talking {
			talking = true
			g.startTalking()
		}
		if time.Since(last) >= 120*time.Millisecond {
			last = time.Now()
			waiting.Stop()
			g.showBalloon(strings.TrimSpace(acc))
			shown = true
		}
	}
	onTool := func(ev hermes.ToolEvent) {
		wake()
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(ev.Name, "face") || strings.HasSuffix(ev.Name, "clip"):
			faced = true
		case strings.HasSuffix(ev.Name, "draw"):
			drew = true
		}
	}

	reply, err := b.turner.Turn(ctx, hermes.TurnRequest{
		Input:   t.input,
		Context: g.turnContext(time.Now()),
		OnDelta: onDelta,
		OnTool:  onTool,
	})
	g.stopTalking()
	waiting.Stop()
	after := func(spoke bool) {
		if t.after != nil {
			t.after(spoke)
		}
	}
	if err != nil {
		if timedOut.Load() {
			err = fmt.Errorf("the brain said nothing for %s — is hermes running?", g.firstTokenTimeout())
		}
		b.fail(t, err)
		return
	}

	text := strings.TrimSpace(reply.Text)
	silent := hermes.Silent(text)
	muted, _ := g.quiet.Active()
	g.noteWhy(Why{
		Time:       time.Now(),
		User:       t.input,
		SelfPrompt: !t.person || hermes.IsEnvelope(t.input),
		Reply:      text,
		Suppressed: silent,
		Acted:      reply.Tools,
		Window:     g.currentWindow(),
	})
	if silent {
		// Nothing was said, so nobody was interrupted, so nothing is owed.
		t.refund()
		mu.Lock()
		wasShown := shown
		mu.Unlock()
		if wasShown {
			g.hideBalloon()
		}
		g.log.Debug(hlog.Brain, "the ghost chose to say nothing")
		after(false)
		return
	}
	mu.Lock()
	drewNow, facedNow := drew, faced
	mu.Unlock()
	if !facedNow {
		// The model wears a face by calling the tool. When it did not, the
		// lexicon is the free fallback, as for scripted lines.
		g.inferExpression(text)
	}
	// Whether it reaches the screen is decided at the screen, as for every
	// line: a quiet hour holds it back, and a reminder held back is still
	// owed. A half-typed line holds it until the line closes, and it has
	// reached the screen when it is shown then.
	if !muted && g.holdLine(text, func() { after(true) }) {
		if userDriven {
			g.markPrompted()
		}
		return
	}
	reached := !muted
	if drewNow && g.pictureShowing() {
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
}

// fail ends a turn that did not happen: nothing was said, so nothing is
// owed, and a person waiting is told why — as is one who watched the ghost
// try to wake up.
func (b *hermesBrain) fail(t hermesTurn, err error) {
	g := b.g
	g.log.Warn(hlog.Brain, "hermes turn: %v", err)
	t.refund()
	if t.person || t.boot {
		g.showBalloon("(that didn't work: " + err.Error() + ")")
		g.lingerBalloon(g.balloonGen(), lingerError)
	}
	if t.after != nil {
		t.after(false)
	}
}

// Close ends the worker, waits briefly for the turn in flight, and stops the
// gateway.
func (b *hermesBrain) Close() error {
	close(b.turns)
	var err error
	select {
	case <-b.done:
	case <-time.After(3 * time.Second):
		err = fmt.Errorf("a hermes turn did not finish")
	}
	if b.stop != nil {
		if serr := b.stop(); serr != nil && err == nil {
			err = serr
		}
	}
	return err
}

// turnContext is the ambient context layered on a turn: the time, what the
// ghost can see when it is allowed to see anything, and anything whose
// moment came without it being said.
//
// The last is what keeps a reminder from being lost. An intention_due turn
// can be dropped (another turn was in flight), answered with silence, or
// held back by a quiet hour; the errand is then still owed, and nothing
// would raise it again. Carried here, the model sees it on every turn
// until it resolves it.
func (g *Ghost) turnContext(now time.Time) string {
	line := "Now: " + now.Format("Mon 2 Jan 15:04") + " (" + partOfDay(now) + ")."
	if w := g.currentWindow().line(); w != "" && g.capabilityActive("window") {
		line += " The person's focused window: " + w + "."
	}
	var owed []string
	for _, it := range g.loadIntentions() {
		if !it.Fired || it.Aside {
			continue
		}
		if goal, ok := g.quietGoal(it); ok {
			owed = append(owed, fmt.Sprintf("their goal %q has gone quiet (%s)", goal.Text, it.Name))
			continue
		}
		owed = append(owed, fmt.Sprintf("%q (%s)", it.Text, it.Name))
	}
	if len(owed) > 0 {
		line += "\nCame due and not yet dealt with: " + strings.Join(owed, "; ") +
			". Raise it when it fits, then resolve it."
	}
	return line
}

func partOfDay(t time.Time) string {
	switch h := t.Hour(); {
	case h < 5:
		return "night"
	case h < 12:
		return "morning"
	case h < 18:
		return "afternoon"
	case h < 22:
		return "evening"
	default:
		return "night"
	}
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
	return hermes.Envelope("goal_quiet",
		hermes.F("name", finding.Name),
		hermes.F("goal", goal.Text),
		hermes.F("goal_name", goal.Name),
		hermes.F("held", roughly(now.Sub(goal.Made))),
		hermes.F("last_seen", seen))
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

// HermesSessionStoreKey keeps the ghost's hermes session id across
// restarts. Exported for `haunt history`, which reads the conversation from
// hermes when a ghost has one.
const HermesSessionStoreKey = internalKeyPrefix + "hermes_session"

// hermesReadyWait is how long a turn waits for a gateway still starting.
// Hermes takes a few seconds to come up, and the boot greeting is sent
// meanwhile; past this, the turn fails with a line in the balloon.
const hermesReadyWait = 2 * time.Minute

// lateTurner is the brain's turner before the gateway is up. The brain
// starts with the ghost, but the gateway is started only once the control
// socket is listening — hermes spawns `haunt mcp-serve` the moment it
// starts, and that asks the socket for the tool list straight away.
type lateTurner struct {
	ready chan struct{}
	once  sync.Once
	t     hermes.Turner
	err   error
}

func newLateTurner() *lateTurner { return &lateTurner{ready: make(chan struct{})} }

// set supplies the real turner, or why there is none.
func (l *lateTurner) set(t hermes.Turner, err error) {
	l.once.Do(func() {
		l.t, l.err = t, err
		close(l.ready)
	})
}

// Up reports whether the gateway has finished starting, well or badly.
func (l *lateTurner) Up() bool {
	select {
	case <-l.ready:
		return true
	default:
		return false
	}
}

// Ready waits for the gateway.
func (l *lateTurner) Ready(ctx context.Context) error {
	wait := time.NewTimer(hermesReadyWait)
	defer wait.Stop()
	select {
	case <-l.ready:
		return l.err
	case <-ctx.Done():
		return ctx.Err()
	case <-wait.C:
		return fmt.Errorf("hermes is still starting")
	}
}

func (l *lateTurner) Turn(ctx context.Context, req hermes.TurnRequest) (hermes.Reply, error) {
	if err := l.Ready(ctx); err != nil {
		return hermes.Reply{}, err
	}
	return l.t.Turn(ctx, req)
}

// startHermes makes the ghost's profile and brings its gateway up, then
// hands the client to the brain. On its own goroutine: hermes takes
// seconds to start, and the ghost is on screen meanwhile.
func (g *Ghost) startHermes(late *lateTurner) {
	g.go_(hlog.Brain, "starting hermes", func() {
		gw, client, err := g.openHermes()
		if err != nil {
			g.log.Error(hlog.Brain, "hermes: %v", err)
			late.set(nil, err)
			return
		}
		g.hermesMu.Lock()
		stopping := false
		select {
		case <-g.stoppingC():
			stopping = true
		default:
			g.hermesGW = gw
		}
		g.hermesMu.Unlock()
		if stopping {
			// Up just as the ghost is going: nobody will stop it later.
			gw.Stop()
			late.set(nil, fmt.Errorf("the ghost is stopping"))
			return
		}
		late.set(client, nil)
		g.log.Info(hlog.Brain, "hermes is up for %s at %s", g.manifest.Ghost.Name, client.BaseURL)
	})
}

func (g *Ghost) openHermes() (*hermes.Gateway, *hermes.Client, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	name := g.manifest.Ghost.Name
	env := map[string]string{}
	// Hermes hands MCP servers a filtered environment; XDG_RUNTIME_DIR
	// survives it and HAUNT_RUNTIME_DIR does not, and the socket is found
	// by one or the other.
	if dir := os.Getenv("HAUNT_RUNTIME_DIR"); dir != "" {
		env["HAUNT_RUNTIME_DIR"] = dir
	}
	profile, err := g.hermesCfg.EnsureProfile(hermes.ProfileSpec{
		Ghost:   name,
		Persona: g.hermesPersona(),
		Serve:   []string{self, "mcp-serve", "--ghost", name},
		Env:     env,
		Skills:  g.hermesSkills(),
	})
	if err != nil {
		return nil, nil, err
	}
	gw := &hermes.Gateway{
		Config:  g.hermesCfg,
		Profile: profile,
		Log:     &logLines{log: g.log},
		Logf:    func(f string, a ...any) { g.log.Warn(hlog.Brain, f, a...) },
	}
	client, err := gw.Start(g.sessionContext())
	if err != nil {
		return nil, nil, err
	}
	client.LoadSession = func() string {
		id, _, _ := g.store.Get(HermesSessionStoreKey)
		return id
	}
	client.SaveSession = func(id string) {
		if err := g.store.Set(HermesSessionStoreKey, id); err != nil {
			g.log.Warn(hlog.Store, "saving the hermes session: %v", err)
		}
	}
	return gw, client, nil
}

// hermesSkills is what the profile's skills are installed from: the ghost
// package's skills/ directory, if it has one, haunt's own, and the
// bundled hermes skills the manifest names. The package's come first, so
// a persona author can replace one of haunt's routines by writing a skill
// of the same name.
func (g *Ghost) hermesSkills() []hermes.SkillSource {
	return []hermes.SkillSource{
		{
			Name:  "ghost",
			About: "Routines that came with " + g.manifest.Ghost.Name + ".",
			FS:    os.DirFS(filepath.Join(g.dir, "skills")),
		},
		{
			Name:  "haunt",
			About: "Routines for a desktop ghost, done with your haunt tools.",
			FS:    skills.Haunt,
		},
		{
			// Named like the skills hermes seeds itself, so the journal
			// lists them together.
			Name:  hermes.SkillHermes,
			About: "Know-how from hermes's own library.",
			FS:    hermesskills.Only(g.manifest.Hermes.BundledSkills),
		},
	}
}

// hermesPersona is what SOUL.md is written from.
func (g *Ghost) hermesPersona() hermes.Persona {
	p := hermes.Persona{Name: g.manifest.Ghost.Name, System: g.manifest.Persona.System}
	if g.shell != nil {
		p.Poses = g.shell.Poses()
		for _, c := range g.shell.Clips() {
			p.Clips = append(p.Clips, c.Name)
		}
	}
	for _, c := range []struct{ cap, what string }{
		{"imagine", "paint a picture in your balloon"},
		{"screen", "look at the window they have open"},
		{"tell", "speak to the other ghosts"},
		{"tools", "use the tools listed in your schemas to do things for them"},
	} {
		if g.declares(c.cap) {
			p.Abilities = append(p.Abilities, c.what)
		}
	}
	return p
}

// logLines passes the gateway's output to the log, a line at a time.
type logLines struct {
	log     *hlog.Logger
	partial []byte
}

func (w *logLines) Write(b []byte) (int, error) {
	w.partial = append(w.partial, b...)
	for {
		i := bytes.IndexByte(w.partial, '\n')
		if i < 0 {
			break
		}
		if line := strings.TrimSpace(string(w.partial[:i])); line != "" {
			w.log.Debug(hlog.Brain, "hermes: %s", line)
		}
		w.partial = w.partial[i+1:]
	}
	return len(b), nil
}
