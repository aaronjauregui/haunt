package ghost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aaronjauregui/haunt/internal/config"
	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/memory"
	"github.com/aaronjauregui/haunt/internal/model"
)

// Reflection: once a day is over, the ghost looks back on it, keeps any
// fact about the person it did not keep at the time, and writes the day's
// diary entry (diary.go). It is how a day reaches the next ones once its
// turns leave the prompt, and it runs once per day.
//
// When: on the worker, after a turn, when the person goes idle, and every
// reflectTick, if the live transcript has a day before today that the
// person and the ghost talked on and that has no entry; the person is
// idle or away; and nothing is queued or waiting. One day per run, oldest
// first. It reaches no screen, so it is not charged to initiative. The
// person's words come first: the worker looks at the queue between
// rounds, and the model call is cancelled the moment words queue
// (interrupt). Compaction also reflects on a day it needs to fold that
// has no entry, whether or not the person is there.
//
// The turn reads what the day read: every web or file result of the day
// taints it and is one of its reads (readsOfDay), so what it keeps is
// labelled web or file and, on cloud, a write in a page's words is
// refused, as on the turn that read the page. diary writes under the day
// the turn is about and never over an entry already there.
//
// How, by [memory] reflection:
//
//   - character (the default): a turn of the ghost's own, kind=reflect,
//     with the day's digest as its context, offered the memory decisions,
//     diary and stay_silent and nothing that reaches the screen. Its words,
//     if any, are dropped; a diary entry written ends it.
//   - side: one call with thinking off and no tools, asked for JSON (the
//     entry, the facts, the lines to forget), which the driver applies
//     through the same decisions and gates as a turn of kind=reflect.
//
// In both, the turn may retire a line the person stated (mayRetire): it
// weighs the whole day the person spoke in, which no page or event turn
// does. The new line keeps the turn's own provenance.
//
// Either way the turn is written to the transcript, so `haunt history`
// shows what reflection kept, and compacted as it ends, so it is never
// replayed or summarised. Its calls go to the slot of the calls without
// tools ([model] slots), leaving the decide slot's prompt for the person.

// reflectKind is the reflection turn's event kind.
const reflectKind = "reflect"

// diaryName is the decision that writes a day's entry.
const diaryName = "diary"

// reflectRule is the reflection turn's standing rule, carried in its
// context with the digest rather than in part 1, which stays the same.
const reflectRule = "Write the day's entry in the first person, under 600 characters: what they told you, " +
	"what you took on, what you talked about. Keep names, dates and numbers. Before it, keep any fact " +
	"about them you have not kept, with replaces when it makes an old line false."

// reflectOffer is what a reflection turn may decide: the memory
// decisions, the entry, and nothing.
var reflectOffer = []string{"remember", "forget", "recall", "rewrite_notes", diaryName, silentName}

// reflectPrompt is the side call's system message, in the shape of the
// compaction prompt: the driver asking, in the second person, about the
// ghost's own day.
const reflectPrompt = "You are looking back on one day of your own conversation with the person you live with, " +
	"to keep what matters from it. Answer with JSON only, in this shape: " +
	`{"diary": "...", "facts": [{"topic": "...", "line": "...", "replaces": "..."}], "forget": [{"topic": "...", "line": "..."}]}. ` +
	"diary is the day's entry in the first person, under 600 characters: what they told you, what you took on, " +
	"what you talked about. Keep names, dates and numbers exactly. facts are things about them the day told you " +
	"that you do not remember yet, one short line each under a one-word topic; when one of the topics you " +
	"remember fits, use it rather than a new one. replaces is a line you remember that the fact makes false, " +
	"or empty. forget is lines you remember that the day showed are wrong. In replaces and forget, write the " +
	"line's text only, as it follows its topic and the colon, without the topic. " +
	"Leave out what you looked up unless they asked you to keep it. No preamble."

// errInterrupted ends a reflection the person's words came in on.
var errInterrupted = errors.New("the person spoke")

// reflectTick is how often the worker looks for a day to reflect on with
// nothing else to wake it: an idle that began before midnight sends no
// second idle event after it.
const reflectTick = 15 * time.Minute

// reflectFailures is how many model or transport errors a day may have
// before reflection gives up on it for the process, so a request the
// server refuses every time does not run every reflectTick all night.
const reflectFailures = 3

// reflectFacts bounds what the side call is shown of the ghost's memory.
const reflectFacts = 3000

// reflect writes the entry for the oldest finished day that needs one,
// when the person is away and nothing waits.
func (b *modelBrain) reflect() {
	if !b.mayReflect() {
		return
	}
	if day, ok := b.reflectDue(time.Now()); ok {
		b.reflectDay(day, true)
	}
}

// mayReflect is the idle gate: the person idle or away, nothing queued or
// deferred.
func (b *modelBrain) mayReflect() bool {
	if b.mem == nil || b.memDir == "" || len(b.turns) > 0 || !b.g.personAway() {
		return false
	}
	b.deferMu.Lock()
	defer b.deferMu.Unlock()
	return len(b.deferred) == 0
}

// personAway is whether the compositor says the person is idle, or a
// presence poll says nobody is home.
func (g *Ghost) personAway() bool {
	if g.idle.Load() {
		return true
	}
	return g.quiet != nil && g.quiet.NobodyHome()
}

// reflectDue is the oldest day before today that was talked on, has no
// entry, and has not come to nothing already in this process.
func (b *modelBrain) reflectDue(now time.Time) (string, bool) {
	rows, err := b.tr.live()
	if err != nil {
		return "", false
	}
	today := dayOf(now)
	for _, day := range daysOf(rows) {
		if day >= today {
			break
		}
		if b.tried[day] || !talked(rows, day) {
			continue
		}
		if _, ok := b.diaryEntry(day); !ok {
			return day, true
		}
	}
	return "", false
}

// interrupt cancels the reflection in flight, if there is one.
func (b *modelBrain) interrupt() {
	b.reflectMu.Lock()
	defer b.reflectMu.Unlock()
	if b.reflectCancel != nil {
		b.reflectCancel()
	}
}

// nudge asks the worker to look for a day to reflect on.
func (b *modelBrain) nudge() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// arm makes cancel the one interrupt calls; disarm takes it back.
func (b *modelBrain) arm(cancel context.CancelFunc) {
	b.reflectMu.Lock()
	b.reflectCancel = cancel
	b.reflectMu.Unlock()
}

func (b *modelBrain) disarm() { b.arm(nil) }

// reflectDay runs one reflection on day, by the configured mode. away
// says it ran because the person is away, and stops when they come back;
// compaction's reflection runs with them there. A day whose reflection ran
// to its end with no entry is not tried again in this process, nor one
// that failed reflectFailures times on a model or transport error; one the
// person interrupted is. The turn's rows are compacted when it ends: it is
// never replayed, and must not count toward the turns kept verbatim.
func (b *modelBrain) reflectDay(day string, away bool) {
	g := b.g
	if b.mem == nil || b.memDir == "" || !g.capabilityActive("llm") {
		return
	}
	// Armed first, so words queued from here on cancel it.
	ctx, cancel := context.WithCancel(g.sessionContext())
	b.arm(cancel)
	defer func() {
		b.disarm()
		cancel()
	}()
	if len(b.turns) > 0 {
		return
	}
	rows, err := b.tr.live()
	if err != nil {
		return
	}
	head, lines := digestLines(rows, day)
	if len(head) == 0 && len(lines) == 0 {
		return
	}

	t := &turn{input: Envelope(reflectKind, F("day", day), F("weekday", weekday(day))), refund: noRefund}
	if id, err := b.tr.nextTurn(); err == nil {
		t.id = id
	}
	g.cur.Store(t)
	defer g.cur.CompareAndSwap(t, nil)
	b.readsOfDay(t, day)
	b.record(t, TranscriptRow{Kind: rowEvent, Text: t.input})
	defer func() {
		if err := b.tr.compactTurn(t.id); err != nil {
			g.log.Warn(hlog.Brain, "putting away the reflection turn: %v", err)
		}
	}()

	mode := g.reflection
	if mode == config.ReflectSide {
		err = b.reflectSide(ctx, t, day, head, lines)
	} else {
		mode = config.ReflectCharacter
		err = b.reflectCharacter(ctx, t, head, lines, away)
	}
	switch {
	case errors.Is(err, errInterrupted) || (err != nil && ctx.Err() != nil):
		b.record(t, TranscriptRow{Kind: rowFailed, Text: "interrupted: the person spoke"})
		g.log.Debug(hlog.Brain, "reflecting on %s: the person spoke; it waits for the next idle", day)
		return
	case err != nil:
		b.record(t, TranscriptRow{Kind: rowFailed, Text: err.Error()})
		g.log.Warn(hlog.Brain, "reflecting on %s: %v", day, err)
		if b.failed[day]++; b.failed[day] >= reflectFailures {
			b.tried[day] = true
			g.log.Warn(hlog.Brain, "reflecting on %s failed %d times; not trying it again until haunt restarts", day, b.failed[day])
		}
		return
	}
	// Its reasons, as run keeps them for a turn that ends: without them
	// `haunt history -why` would find a silent row with none of its own.
	// A failed reflection, like a failed turn, keeps none.
	acted, _, _ := t.marks()
	g.noteWhy(Why{
		Time:       time.Now(),
		User:       t.input,
		SelfPrompt: true,
		Suppressed: true,
		Acted:      acted,
		Window:     g.currentWindow(),
	})
	b.record(t, TranscriptRow{Kind: rowSilent})
	if _, ok := b.diaryEntry(day); ok {
		g.log.Info(hlog.Memory, "wrote the diary for %s (%s)", day, mode)
		return
	}
	b.tried[day] = true
	g.log.Info(hlog.Memory, "reflecting on %s came to no diary entry (%s)", day, mode)
}

// readsOfDay gives the reflection turn what the day read: every result of
// a read that brought somebody else's words in (taintingReads), compacted
// or not, taints the turn and is kept as one of its reads. Looking back
// on a page is reading it again, so what the reflection keeps is labelled
// web or file, and on cloud the echo check refuses a write in its words,
// as on the turn that read it.
func (b *modelBrain) readsOfDay(t *turn, day string) {
	all, err := b.tr.all()
	if err != nil {
		return
	}
	for _, r := range rowsOf(all, day) {
		src, ok := taintingReads[r.Tool]
		if r.Kind != rowResult || !ok || strings.HasPrefix(r.Text, "That was not allowed") ||
			strings.HasPrefix(r.Text, "That did not work") {
			continue
		}
		t.taint(src)
		t.reads.add(r.Tool, r.Text)
	}
}

// reflectDecisions is what this ghost may decide on a reflection turn.
// The turn must be the one in flight, for diary's offeredTo.
func (b *modelBrain) reflectDecisions() map[string]decision {
	all := b.g.offered(false)
	out := map[string]decision{}
	for _, name := range reflectOffer {
		if d, ok := all[name]; ok {
			out[name] = d
		}
	}
	return out
}

// reflectReply is the room, in tokens, a character reflection keeps free
// for the model's calls and their results, over every round.
const reflectReply = 2048

// reflectCharacter is the reflection as a turn of the ghost's own: the
// prompt as for any turn, with the rule and the day in place of the
// turn's context, and a decide loop that ends on diary or stay_silent.
// The digest gets 60% of the room the prompt and tools leave, and never
// eats into reflectReply, so later rounds fit without measuring again; a
// prompt that alone takes 80% of the context gets digestMin. Its calls go
// to the slot of the calls without tools, so the next person's turn still
// finds its decide prompt cached. With away, it stops between rounds once
// the person is back.
func (b *modelBrain) reflectCharacter(ctx context.Context, t *turn, head, lines []string, away bool) error {
	g := b.g
	msgs, err := b.prompt(t, time.Now())
	if err != nil {
		return err
	}
	ds := b.reflectDecisions()
	tools := toolsOf(ds)
	lead := reflectRule + "\n\nThe day, as it went:\n"
	tail := "\n\n" + g.standing(b.cfg.Fold()).reminder()
	msgs[len(msgs)-1] = model.Message{Role: "system", Content: lead + tail}
	msgs[len(msgs)-1].Content = lead + digestWithin(head, lines, digestRoom(contextLength(b.cfg.ContextLength),
		estimateTokens(msgs)+toolTokens(tools))*4) + tail
	for round := range maxRounds {
		if len(b.turns) > 0 || (away && round > 0 && !g.personAway()) {
			return errInterrupted
		}
		r, err := stream(ctx, b.client, model.Request{
			Messages: msgs,
			Tools:    tools,
			Thinking: false,
			Sampling: g.manifest.Persona.Sampling,
			Slot:     b.speakSlot,
		}, nil)
		if err == nil {
			// A cancelled call ends its stream without an error.
			err = ctx.Err()
		}
		if err != nil {
			return err
		}
		if r.usage != nil {
			g.log.Debug(hlog.Brain, "reflect: %d prompt tokens (%d cached), %d out",
				r.usage.PromptTokens, r.usage.CachedTokens, r.usage.CompletionTokens)
		}
		if len(r.calls) == 0 {
			calls, rest, found, err := textCalls(r.text, ds)
			switch {
			case found && err == nil:
				r.calls, r.text = calls, rest
			case found:
				g.log.Warn(hlog.Brain, "the model wrote a call as text that could not be read (%v); "+
					"it is dropped: %s", err, clip(r.text, 160))
			}
		}
		if len(r.calls) == 0 {
			return nil // words, with nobody to say them to: dropped
		}
		row := TranscriptRow{Kind: rowCalls, Text: r.text}
		for _, c := range r.calls {
			row.Calls = append(row.Calls, TranscriptCall{ID: c.ID, Name: c.Name, Args: c.Args})
		}
		b.record(t, row)
		msgs = append(msgs, model.Message{Role: "assistant", Content: r.text, ToolCalls: r.calls})
		done := false
		for _, c := range r.calls {
			res := toolResult{Text: "Not done: you had already decided."}
			if !done {
				res = g.callDecision(ctx, t, ds, c.Name, c.Args)
				done = c.Name == silentName || (c.Name == diaryName && !res.IsError)
			}
			msgs = append(msgs, model.Message{Role: "tool", Content: res.Text, ToolCallID: c.ID, Name: c.Name})
			b.record(t, TranscriptRow{Kind: rowResult, Tool: c.Name, CallID: c.ID, Text: res.Text})
		}
		if done {
			return nil
		}
	}
	return nil
}

// digestRoom is the tokens a character reflection's digest may take in a
// context of ctx tokens whose prompt and tools take used: 60% of what is
// left, and never reflectReply of it; none past 80% used, which leaves
// digestWithin's floor.
func digestRoom(ctx, used int) int {
	if used*5 > ctx*4 {
		return 0
	}
	free := ctx - used
	return max(0, min(free*3/5, free-reflectReply))
}

// contextLength is [model] context_length, or the default.
func contextLength(n int) int {
	if n <= 0 {
		return defaultContext
	}
	return n
}

// toolTokens is a rough count of what the tools' declarations cost.
func toolTokens(tools []model.Tool) int {
	n := 0
	for _, tl := range tools {
		n += 4 + (len(tl.Name)+len(tl.Description)+len(tl.Schema))/4
	}
	return n
}

// reflection is the side call's answer.
type reflection struct {
	Diary string `json:"diary"`
	Facts []struct {
		Topic    string `json:"topic"`
		Line     string `json:"line"`
		Replaces string `json:"replaces,omitempty"`
	} `json:"facts"`
	Forget []struct {
		Topic string `json:"topic"`
		Line  string `json:"line"`
	} `json:"forget"`
}

// parseReflection reads the JSON object in the side call's answer, past
// any fence or words around it.
func parseReflection(text string) (reflection, error) {
	var r reflection
	i, j := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if i < 0 || j < i {
		return r, fmt.Errorf("the answer held no JSON: %s", clip(text, 120))
	}
	if err := json.Unmarshal([]byte(text[i:j+1]), &r); err != nil {
		return r, fmt.Errorf("the answer did not read as JSON: %v", err)
	}
	return r, nil
}

// reflectSide is the reflection as one side call whose answer is applied
// as the ghost's decisions on t: the facts, then what to forget, then the
// entry, each through callDecision, a refusal logged. Only the call can be
// interrupted:
// once it has answered, applying the answer is local and immediate.
func (b *modelBrain) reflectSide(ctx context.Context, t *turn, day string, head, lines []string) error {
	g := b.g
	standing := estimateTokens([]model.Message{
		{Content: g.standing(b.cfg.Fold()).system()}, {Content: b.notes()}, {Content: b.indexes()},
	})
	user := "The day: " + weekday(day) + " " + day + "\n" +
		digestWithin(head, lines, (compactBudget(b.cfg.ContextLength)-standing)*4)
	if kept := b.keptFacts(); kept != "" {
		user += "\n\nWhat you remember now, as topic: line:\n" + kept
	}
	r, err := stream(ctx, b.client, model.Request{
		Messages: []model.Message{
			{Role: "system", Content: reflectPrompt},
			{Role: "user", Content: user},
		},
		Thinking: false,
		Slot:     b.speakSlot,
	}, nil)
	b.disarm()
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return err
	}
	ans, err := parseReflection(r.text)
	if err != nil {
		// The model answered, with nothing to apply: a day tried.
		g.log.Warn(hlog.Brain, "reflecting on %s: %v", day, err)
		return nil
	}
	var calls []model.ToolCall
	add := func(name string, args any) {
		raw, _ := json.Marshal(args)
		calls = append(calls, model.ToolCall{ID: fmt.Sprintf("reflect-%d", len(calls)+1), Name: name, Args: raw})
	}
	for _, f := range ans.Facts {
		add("remember", f)
	}
	for _, f := range ans.Forget {
		add("forget", f)
	}
	if strings.TrimSpace(ans.Diary) != "" {
		add(diaryName, map[string]string{"day": day, "text": ans.Diary})
	}
	ds := b.reflectDecisions()
	var results []TranscriptRow
	for _, c := range calls {
		res := g.callDecision(ctx, t, ds, c.Name, c.Args)
		if res.IsError {
			g.log.Info(hlog.Memory, "reflecting on %s: %s %s: %s", day, c.Name, clip(string(c.Args), 160), res.Text)
		}
		results = append(results, TranscriptRow{Kind: rowResult, Tool: c.Name, CallID: c.ID, Text: res.Text})
	}
	if len(calls) == 0 {
		return nil
	}
	row := TranscriptRow{Kind: rowCalls}
	for _, c := range calls {
		row.Calls = append(row.Calls, TranscriptCall{ID: c.ID, Name: c.Name, Args: c.Args})
	}
	b.record(t, row)
	for _, r := range results {
		b.record(t, r)
	}
	return nil
}

// keptFacts is the ghost's topics and their lines, "where: lives in
// Sydney", for the side call to name a line it replaces or forgets and to
// file a fact under a topic it has, cut at reflectFacts bytes. Nothing
// that looks like part of the line ("[where] ") is put before it: Gemma
// copied that into replaces.
func (b *modelBrain) keptFacts() string {
	topics, err := memory.Topics(b.memDir)
	if err != nil {
		return ""
	}
	var s strings.Builder
	for _, tp := range topics {
		facts, err := b.mem.Facts(tp.Name)
		if err != nil {
			continue
		}
		for _, f := range facts {
			line := tp.Name + ": " + f.Line + "\n"
			if s.Len()+len(line) > reflectFacts {
				return s.String() + "(and more)"
			}
			s.WriteString(line)
		}
	}
	return strings.TrimRight(s.String(), "\n")
}

// digestLines is the day's rows as the reflection reads them
// (digestLine): head is the day's summary, lines the rest, in order.
func digestLines(rows []TranscriptRow, day string) (head, lines []string) {
	for _, r := range rowsOf(rows, day) {
		line := digestLine(r)
		switch {
		case line == "":
		case r.Kind == rowSummary:
			head = append(head, line)
		default:
			lines = append(lines, line)
		}
	}
	return head, lines
}

// digestMin is the least a digest is allowed, in bytes, however little
// room the prompt leaves.
const digestMin = 4000

// digestWithin joins head and lines within limit bytes, dropping the
// earliest lines first and saying so.
func digestWithin(head, lines []string, limit int) string {
	limit = max(limit, digestMin)
	size := 0
	for _, h := range head {
		size += len(h) + 1
	}
	from := len(lines)
	for from > 0 && size+len(lines[from-1])+1 <= limit {
		from--
		size += len(lines[from]) + 1
	}
	out := append([]string(nil), head...)
	if from > 0 {
		out = append(out, "(earlier in the day, left out)")
	}
	return strings.Join(append(out, lines[from:]...), "\n")
}
