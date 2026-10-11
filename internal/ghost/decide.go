package ghost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/model"
)

// Deciding, then speaking.
//
// A turn is two kinds of call to the model. The decide call offers the
// registry below as tools and loops: each tool call is put through the
// driver's gates and applied, its result goes back as a tool message, and
// the loop ends when the model calls a terminal decision. `speak` ends it
// with the face to wear, and the words are then a second call, prose only,
// with the persona's prefill and thinking off. `stay_silent` ends it with
// nothing said. A provider with fold on (cloud by default) takes the words
// from the decide call itself: the reply that calls `speak` carries them as
// its text, so the pose and the words come in one round, and a reply of
// text with no call is the words with the face left as it is.
//
// A model that writes a call out as text instead of making it (Gemma's
// own call syntax, leaked into the content) has the call read back out of
// the text and applied as if it had been made (textCalls), in a decide
// round or in the speak call, where it means the deciding was not over
// (speakCalls). A call ending the turn in the round that opens a skill
// or puts a question waits until the model has read what came back.
//
// The registry is how the ghost grows new behaviour without a new call
// shape. A decision is a name, a description, a JSON schema and an apply
// function; one that ends the loop says so. Adding one is a file with a
// type and an init that registers it. Server tools from [[tool]] are
// decisions too, built per turn from what the running servers offer.
//
// Every decision goes through the same gates whoever wrote it: a decision
// whose capability the package never asked for is not offered at all;
// once somebody else's words have entered the turn, a decision that
// reaches past the person or sends words off the machine is put to the
// person when the trust table says so (trust.go, web.go); and inside
// each, the capability, consent, quiet hours, the fullscreen rule, the
// budgets and the three marks. A refusal is a result the model reads, never an error
// that ends the turn.

// decision is one thing the model can decide to do.
type decision interface {
	name() string
	describe() string
	schema() json.RawMessage
	terminal() bool
	apply(ctx context.Context, g *Ghost, t *turn, args json.RawMessage) (result string, err error)
}

// decisions is the registry, by name.
var decisions = map[string]decision{}

// register adds a decision to the registry. Called from init functions;
// a name registered twice is a bug in the build.
func register(d decision) {
	if _, dup := decisions[d.name()]; dup {
		panic("decision registered twice: " + d.name())
	}
	decisions[d.name()] = d
}

// needsDeclared is implemented by a decision that is offered only to a
// package that asked for a capability. Most decisions are offered to every
// ghost and refused when used; the web and file verbs are not, because a
// skill that needs `search` is hidden when `search` is not offered, which
// is how a ghost with no web stops claiming it can look things up.
type needsDeclared interface{ declared() string }

// taintGated is implemented by a decision that is put to the person once
// the turn has read somebody else's words: which sources gate it, for
// these arguments. 0 means none.
type taintGated interface {
	gatedAfter(args json.RawMessage) taintSource
}

// verb is a decision written as a table entry, which is most of them.
type verb struct {
	id   string
	desc string
	// params is the schema, as "name:type" pairs (schemaOf).
	params []string
	ends   bool
	// needs is the capability the package must declare for the verb to be
	// offered at all (needsDeclared).
	needs string
	// sideEffect marks a verb that reaches past the person in front of the
	// ghost: it messages someone, or writes something that outlives the
	// turn. leaves marks one that sends words off the machine. Both are
	// gated after reading (gatedAfter in web.go).
	sideEffect, leaves bool
	run                func(ctx context.Context, g *Ghost, t *turn, args json.RawMessage) (string, error)
}

func (v verb) name() string            { return v.id }
func (v verb) describe() string        { return v.desc }
func (v verb) schema() json.RawMessage { return schemaOf(v.params...) }
func (v verb) terminal() bool          { return v.ends }
func (v verb) declared() string        { return v.needs }
func (v verb) apply(ctx context.Context, g *Ghost, t *turn, args json.RawMessage) (string, error) {
	return v.run(ctx, g, t, args)
}
func (v verb) gatedAfter(args json.RawMessage) taintSource {
	return gatedAfter(v.id, v.sideEffect, v.leaves, args)
}

// tool adapts one of the ghost's tool functions, which do not need the
// turn, to a verb's run.
func tool(f func(g *Ghost, ctx context.Context, args json.RawMessage) (string, error)) func(context.Context, *Ghost, *turn, json.RawMessage) (string, error) {
	return func(ctx context.Context, g *Ghost, _ *turn, args json.RawMessage) (string, error) {
		return f(g, ctx, args)
	}
}

// schemaOf builds a JSON Schema object from "name:type" pairs; a trailing
// "?" on the name makes it optional.
func schemaOf(props ...string) json.RawMessage {
	type prop struct {
		Type  string          `json:"type"`
		Items json.RawMessage `json:"items,omitempty"`
	}
	p := map[string]prop{}
	required := []string{}
	for _, def := range props {
		name, typ, _ := strings.Cut(def, ":")
		opt := strings.HasSuffix(name, "?")
		name = strings.TrimSuffix(name, "?")
		pr := prop{Type: typ}
		if typ == "array" {
			pr.Items = json.RawMessage(`{"type":"string"}`)
		}
		p[name] = pr
		if !opt {
			required = append(required, name)
		}
	}
	b, _ := json.Marshal(map[string]any{"type": "object", "properties": p, "required": required})
	return b
}

// turn is one thing sent to the model and everything that happens in
// answer. The brain owns it; the decisions applied during it read and
// mark it. One turn is in flight per ghost (g.cur).
type turn struct {
	input string
	// person says someone is waiting on this turn: its words stream into
	// the balloon as they arrive. An event turn's words are held until the
	// model has decided to say something.
	person bool
	// refund puts back what was spent to wake the ghost, when it turns out
	// to have had nothing to say.
	refund func()
	// after runs once the turn is over, with whether anything reached the
	// screen.
	after func(shown bool)
	// waiting is the indicator already up for this turn: a person's turn
	// queued behind something, or the boot turn's waking indicator.
	waiting *waiter
	// boot says this is the greeting, whose failure the person is told
	// about: they were shown the ghost waking up.
	boot bool
	// id is the turn's number in the transcript, and the turn a memory
	// written during it is labelled with.
	id int64

	mu sync.Mutex
	// tainted is where the text nobody in the room wrote came from, if any
	// has entered this turn (web.go).
	tainted taintSource
	// reads is what the turn's reads returned, for the echo check on a
	// write (trust.go).
	reads turnReads
	// acted is the decisions applied, in order, for the why-trace.
	acted []string
	// faced and drew say the model wore a face or painted this turn.
	faced, drew bool
}

// kind is the event kind of a turn nobody started ("occasion", "idle"),
// and empty for the person's own words.
func (t *turn) kind() string { return EnvelopeKind(t.input) }

func (t *turn) taint(src taintSource) {
	t.mu.Lock()
	t.tainted |= src
	t.mu.Unlock()
}

func (t *turn) taintedBy() taintSource {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.tainted
}

func (t *turn) noteActed(name string) {
	if name == speakName || name == silentName {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.acted = append(t.acted, name)
}

// noteDone records what a decision that worked did to the screen: a face
// worn, or a picture put up that the words should not paint over.
func (t *turn) noteDone(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch name {
	case "face", "clip":
		t.faced = true
	case "draw", "show":
		t.drew = true
	}
}

func (t *turn) hasDrawn() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.drew
}

func (t *turn) marks() (acted []string, faced, drew bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.acted...), t.faced, t.drew
}

// current is the turn in flight, or nil between turns.
func (g *Ghost) current() *turn { return g.cur.Load() }

// startTurn makes a turn of the given kind the one in flight. The brain
// starts its own; this is the same for a caller with no brain.
func (g *Ghost) startTurn(person bool, kind string) *turn {
	t := &turn{person: person}
	if kind != "" {
		t.input = Envelope(kind)
	}
	g.cur.Store(t)
	return t
}

// thisTurn reads back what the reading tools need: who started the turn in
// flight, its kind, and what it has read.
func (g *Ghost) thisTurn() (person bool, kind string, tainted taintSource) {
	t := g.current()
	if t == nil {
		return false, "", 0
	}
	return t.person, t.kind(), t.taintedBy()
}

// taint marks the turn in flight as carrying text from src. Only content
// does it: a refusal or a failed read brought nothing in.
func (g *Ghost) taint(src taintSource) {
	if t := g.current(); t != nil {
		t.taint(src)
	}
}

// refusal is an error that means the driver would not, as opposed to could
// not; the model is told which.
type refusal struct{ why string }

func (r refusal) Error() string { return r.why }

func refuse(format string, a ...any) error { return refusal{fmt.Sprintf(format, a...)} }

// toolResult is what a decision came to, as the model reads it.
type toolResult struct {
	Text    string
	IsError bool
}

// refusalResult is how a failure reaches the model: as content it can
// read and answer around, saying whether the driver would not or could
// not.
func refusalResult(err error) toolResult {
	var r refusal
	if errors.As(err, &r) {
		return toolResult{Text: "That was not allowed: " + r.why + ".", IsError: true}
	}
	return toolResult{Text: "That did not work: " + err.Error() + ".", IsError: true}
}

func decode(args json.RawMessage, v any) error {
	if err := json.Unmarshal(args, v); err != nil {
		return fmt.Errorf("bad arguments: %v", err)
	}
	return nil
}

// offered is every decision this ghost may make on a turn, by name: the
// registry less what its package never asked for, with speak described
// for the words in the same reply when they come in the decide call; then
// what its tool servers offer, when it may use them at all.
func (g *Ghost) offered(fold bool) map[string]decision {
	out := map[string]decision{}
	for name, d := range decisions {
		if fold && name == speakName {
			d = foldedSpeak
		}
		if n, ok := d.(needsDeclared); ok && n.declared() != "" && !g.declares(n.declared()) {
			continue
		}
		out[name] = d
	}
	if g.capabilityActive("tools") {
		g.startToolServers()
		specs, back := g.offerable()
		for _, s := range specs {
			out[s.Name] = serverDecision{spec: s, ref: back[s.Name]}
		}
	}
	return out
}

// toolsOf renders decisions as the request's tools, sorted by name so the
// request is the same from turn to turn and the provider's prompt cache
// holds.
func toolsOf(ds map[string]decision) []model.Tool {
	names := make([]string, 0, len(ds))
	for name := range ds {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]model.Tool, 0, len(names))
	for _, name := range names {
		d := ds[name]
		out = append(out, model.Tool{Name: name, Description: d.describe(), Schema: d.schema()})
	}
	return out
}

// callDecision applies one decision through the gates every decision
// shares: the arguments must be JSON, the decision must be one this ghost
// is offered, and once the turn has read somebody else's words a decision
// that reaches past the person is put to them first.
func (g *Ghost) callDecision(ctx context.Context, t *turn, ds map[string]decision, name string, args json.RawMessage) toolResult {
	args = json.RawMessage(strings.TrimSpace(string(args)))
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage(`{}`)
	}
	if !json.Valid(args) {
		return toolResult{Text: "Those arguments are not valid JSON.", IsError: true}
	}
	args = unalias(name, args)
	g.log.Debug(hlog.Tool, "the model decided %s %s", name, clip(string(args), 200))
	d, ok := ds[name]
	if !ok {
		return toolResult{Text: "There is no tool called " + name + ".", IsError: true}
	}
	if tg, ok := d.(taintGated); ok && t != nil {
		if after := g.gateAfterReading(t, name, tg.gatedAfter(args)); after != 0 {
			if err := g.allowAfterReading(t, actSummary(name, args), after); err != nil {
				return refusalResult(err)
			}
		}
	}
	if t != nil {
		t.noteActed(name)
	}
	text, err := d.apply(ctx, g, t, args)
	if err != nil {
		return refusalResult(err)
	}
	if t != nil {
		t.noteDone(name)
		if _, read := taintingReads[name]; read {
			t.reads.add(name, text)
		}
	}
	return toolResult{Text: text}
}

// argAliases are the argument names a model guesses for a decision in
// place of the ones its schema has, by decision: alias to name. Seen from
// Gemma 4 writing calls out as text. Kept to the guesses seen or nearly
// so; a wrong key anywhere else is answered as a failure to retry.
var argAliases = map[string]map[string]string{
	"ask":         {"question": "text"},
	"skill_view":  {"skill": "name"},
	"skill_write": {"skill": "name"},
	"recall":      {"topic": "query"},
}

// unalias renames a decision's guessed argument names to its own, where
// the right name is not also there. Anything that is not an object goes
// as it came.
func unalias(name string, args json.RawMessage) json.RawMessage {
	aliases := argAliases[name]
	if len(aliases) == 0 {
		return args
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(args, &m) != nil {
		return args
	}
	changed := false
	for from, to := range aliases {
		if v, ok := m[from]; ok {
			if _, has := m[to]; !has {
				m[to] = v
				delete(m, from)
				changed = true
			}
		}
	}
	if !changed {
		return args
	}
	out, err := json.Marshal(m)
	if err != nil {
		return args
	}
	return out
}

// call is callDecision on the turn in flight with everything the ghost is
// offered, for a caller outside the decide loop.
func (g *Ghost) call(ctx context.Context, name string, args json.RawMessage) toolResult {
	return g.callDecision(ctx, g.current(), g.offered(false), name, args)
}

// maxRounds bounds the decide loop. A model that keeps calling tools
// without deciding is ended with silence: recall and skill_view are cheap
// enough to run away on.
const maxRounds = 8

// round is one decide call's answer.
type round struct {
	text  string
	calls []model.ToolCall
	usage *model.Usage
}

// stream runs one call, handing each word to onDelta, and gathers the
// rest.
func stream(ctx context.Context, c model.Client, req model.Request, onDelta func(string)) (round, error) {
	ch, err := c.Stream(ctx, req)
	if err != nil {
		return round{}, err
	}
	var r round
	var b strings.Builder
	for ev := range ch {
		switch ev.Kind {
		case model.EventDelta:
			b.WriteString(ev.Delta)
			if onDelta != nil {
				onDelta(ev.Delta)
			}
		case model.EventToolCall:
			r.calls = append(r.calls, *ev.ToolCall)
		case model.EventUsage:
			r.usage = ev.Usage
		case model.EventError:
			r.text = b.String()
			return r, ev.Err
		}
	}
	r.text = b.String()
	return r, nil
}

// ending is how the decide loop ended.
type ending struct {
	// silent: the model chose to say nothing, or ran out of rounds.
	silent bool
	// words is what to say when the decide call carried them (fold, or a
	// round of text with no calls), with pose the face from a speak call
	// beside them, if any; speak is set when the words are still to be
	// written by the speak call.
	words string
	speak bool
	pose  string
	// msgs is the conversation up to the decision, for the speak call.
	msgs []model.Message
}

// decideRound is one decide call.
func (b *modelBrain) decideRound(ctx context.Context, t *turn, msgs []model.Message, tools []model.Tool, p *parts) (round, error) {
	req := model.Request{
		Messages: msgs,
		Tools:    tools,
		// Thinking on the person's turns, where an answer is worth
		// the wait; off for events, which mostly end in silence.
		Thinking: t.person,
		Sampling: b.g.manifest.Persona.Sampling,
	}
	// The person's turn streams as it comes; an event's words wait
	// until the model has decided they are words.
	var onDelta func(string)
	var held *heldCalls
	if t.person {
		held = &heldCalls{out: p.delta}
		onDelta = held.delta
	}
	r, err := stream(ctx, b.client, req, onDelta)
	if held != nil {
		held.flush()
	}
	if r.usage != nil {
		b.g.log.Debug(hlog.Brain, "decide: %d prompt tokens (%d cached), %d out",
			r.usage.PromptTokens, r.usage.CachedTokens, r.usage.CompletionTokens)
	}
	return r, err
}

// decide runs the loop. msgs is the prompt; parts shows words as they
// stream and as rounds end. first, when there is one, is a round the
// model already wrote, taken as the loop's first instead of a call: the
// calls a speak call wrote as text (speakCalls).
func (b *modelBrain) decide(ctx context.Context, t *turn, msgs []model.Message, p *parts, first *round) (ending, error) {
	g := b.g
	fold := b.cfg.Fold()
	ds := g.offered(fold)
	tools := toolsOf(ds)
	for range maxRounds {
		var r round
		if first != nil {
			r, first = *first, nil
		} else {
			var err error
			if r, err = b.decideRound(ctx, t, msgs, tools, p); err != nil {
				return ending{}, err
			}
		}
		if len(r.calls) == 0 {
			calls, rest, found, err := textCalls(r.text, ds)
			switch {
			case found && err == nil:
				// Calls written out as text: made, and the text they
				// were written in is not words.
				g.log.Debug(hlog.Brain, "the model wrote %d call(s) as text; making them", len(calls))
				p.retract(rest)
				r.calls, r.text = calls, rest
			case found:
				g.log.Warn(hlog.Brain, "the model wrote a call as text that could not be read (%v); "+
					"it goes as words: %s", err, clip(r.text, 160))
			}
		}
		if len(r.calls) == 0 {
			// Text and no call: the words. With fold on the face is left
			// as it is; without it the model wrote its reply instead of
			// deciding to, and asking it again would put its own words
			// last, where a server reads them as a prefill to continue.
			if strings.TrimSpace(r.text) == "" {
				return ending{silent: true}, nil
			}
			return ending{words: r.text}, nil
		}
		// Folded, text beside speak is the words, and the pose is speak's.
		// They go on the words row, not on this one, so the transcript
		// reads as it does unfolded and the words are not replayed twice.
		folded := fold && strings.TrimSpace(r.text) != "" && slices.ContainsFunc(r.calls,
			func(c model.ToolCall) bool { return c.Name == speakName })
		rowText := r.text
		if folded {
			rowText = ""
		}
		// The calls, in order. Every call gets its result message, the
		// terminal one included, since the provider expects an answer to
		// each.
		assistant := model.Message{Role: "assistant", Content: rowText, ToolCalls: r.calls}
		row := TranscriptRow{Kind: rowCalls, Text: rowText}
		for _, c := range r.calls {
			row.Calls = append(row.Calls, TranscriptCall{ID: c.ID, Name: c.Name, Args: c.Args})
		}
		b.record(t, row)
		msgs = append(msgs, assistant)
		var end *ending
		// A skill opened, or a question put, in the same round as the call
		// that ends the turn has not been read: the ending waits for it.
		unread := slices.ContainsFunc(r.calls, func(c model.ToolCall) bool {
			return c.Name == skillViewName || c.Name == askName || c.Name == askTextName
		})
		for _, c := range r.calls {
			d, known := ds[c.Name]
			var res toolResult
			switch {
			case end != nil:
				res = toolResult{Text: "Not done: you had already decided."}
			case known && d.terminal() && unread:
				res = toolResult{Text: unreadCue}
			case known && d.terminal():
				e := ending{silent: c.Name != speakName}
				if !e.silent {
					var a struct {
						Pose string `json:"pose"`
					}
					json.Unmarshal(c.Args, &a)
					e.speak, e.pose = true, strings.TrimSpace(a.Pose)
					if folded {
						e.speak, e.words = false, r.text
					}
				}
				end = &e
				res = g.callDecision(ctx, t, ds, c.Name, c.Args)
				if folded && !e.silent && !res.IsError {
					res.Text = saidCue
				}
			default:
				res = g.callDecision(ctx, t, ds, c.Name, c.Args)
			}
			msgs = append(msgs, model.Message{Role: "tool", Content: res.Text, ToolCallID: c.ID, Name: c.Name})
			b.record(t, TranscriptRow{Kind: rowResult, Tool: c.Name, CallID: c.ID, Text: res.Text})
		}
		if end != nil {
			end.msgs = msgs
			return *end, nil
		}
		// Words beside calls that decide nothing are the ghost saying
		// what it is doing: they stay up while the tools run, until the
		// next words replace them.
		p.interim(r.text)
	}
	g.log.Debug(hlog.Brain, "the model made %d rounds of calls without deciding; ending in silence", maxRounds)
	return ending{silent: true}, nil
}

// speakWords is the speak call: the conversation up to the decision, the
// persona's prefill, thinking off and no tools, streamed through p.
func (b *modelBrain) speakWords(ctx context.Context, msgs []model.Message, p *parts) (string, error) {
	prefill := b.g.manifest.Persona.Prefill
	p.delta(prefill)
	held := &heldCalls{out: p.delta}
	r, err := stream(ctx, b.client, model.Request{
		Messages: msgs,
		Prefill:  prefill,
		Thinking: false,
		Sampling: b.g.manifest.Persona.Sampling,
	}, held.delta)
	held.flush()
	if r.usage != nil {
		b.g.log.Debug(hlog.Brain, "speak: %d prompt tokens (%d cached), %d out",
			r.usage.PromptTokens, r.usage.CachedTokens, r.usage.CompletionTokens)
	}
	return prefill + r.text, err
}

// speakCalls reads the calls a speak call wrote as text. The speak call
// offers no tools, so a model that decided to speak before it was done
// (it opened a skill and spoke in the same round, or read the skill and
// still had an ask to put) writes the rest of its calls in its own
// notation where the words go. more says there is a call among them to
// something other than ending the turn, so the deciding is not over; r
// is those calls as a round, with the words before them as its text.
func (b *modelBrain) speakCalls(words string) (r round, more bool) {
	ds := b.g.offered(b.cfg.Fold())
	calls, rest, found, err := textCalls(words, ds)
	if !found || err != nil {
		return round{}, false
	}
	for _, c := range calls {
		if d, ok := ds[c.Name]; ok && !d.terminal() {
			more = true
		}
	}
	return round{text: rest, calls: calls}, more
}

// beforeCalls is the words of a speak call without any call written after
// them, which are not for the balloon: a call only to speak (the pose
// again), or one that will not read.
func beforeCalls(words string) string {
	if i := strings.Index(words, gemmaCallOpen); i >= 0 {
		words = words[:i]
	}
	return strings.TrimSpace(strings.ReplaceAll(words, gemmaCallClose, ""))
}

// DecisionNames is every decision in the registry, sorted: what a skill
// may name as a tool. For `haunt journal -skills`.
func DecisionNames() []string {
	names := make([]string, 0, len(decisions))
	for name := range decisions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Calls written as text.
//
// Gemma 4 writes a call as `<|tool_call>call:NAME{ARGS}<tool_call|>`, with
// ARGS in its own notation: keys unquoted, strings between `<|"|>` tokens.
// llama-server turns that into a structured call when it can read it;
// when it cannot, the call arrives as the reply's text. textCalls reads it
// back:
//
//	text = [words] call {call}
//	call = ["<|tool_call>"] ["call:"] NAME object ["<tool_call|>"]
//
// NAME is a decision on offer, and object is JSON with three loosenings:
// `<|"|>…<|"|>` is a string, a bare word as a key or a value is a string,
// and true, false, null and numbers are themselves. Words may come before
// the first call only when it opens with `<|tool_call>`, which nobody
// writes by accident; otherwise the text must open with NAME{ or
// call:NAME{. A stray `<tool_call|>` in the words is dropped.

const (
	gemmaCallOpen  = "<|tool_call>"
	gemmaCallClose = "<tool_call|>"
	gemmaQuote     = `<|"|>`
)

// textCalls reads calls written as text. found says the text has a call's
// shape; err, that it had the shape and could not be read. rest is the
// words before the calls.
func textCalls(text string, ds map[string]decision) (calls []model.ToolCall, rest string, found bool, err error) {
	s := strings.TrimSpace(text)
	if at := strings.Index(s, gemmaCallOpen); at >= 0 {
		rest, s = s[:at], s[at:]
	} else if name, _ := callName(strings.TrimPrefix(s, "call:")); ds[name] == nil {
		return nil, "", false, nil
	}
	rest = strings.TrimSpace(strings.ReplaceAll(rest, gemmaCallClose, ""))
	for s != "" {
		s = strings.TrimPrefix(s, gemmaCallOpen)
		s = strings.TrimPrefix(s, "call:")
		name, n := callName(s)
		if name == "" {
			return nil, "", true, fmt.Errorf("no call name at %q", clip(s, 40))
		}
		if ds[name] == nil {
			return nil, "", true, fmt.Errorf("there is no tool called %s", name)
		}
		args, m, err := looseObject(s[n:])
		if err != nil {
			return nil, "", true, fmt.Errorf("%s: %w", name, err)
		}
		calls = append(calls, model.ToolCall{ID: fmt.Sprintf("text_call_%d", len(calls)), Name: name, Args: args})
		s = strings.TrimSpace(s[n+m:])
		s = strings.TrimSpace(strings.TrimPrefix(s, gemmaCallClose))
	}
	return calls, rest, true, nil
}

// callName is the name at the start of s, up to an opening brace, and how
// many bytes it takes; "" when s does not start NAME{.
func callName(s string) (string, int) {
	i := 0
	for i < len(s) && isNameByte(s[i]) {
		i++
	}
	if i == 0 || i >= len(s) || s[i] != '{' {
		return "", 0
	}
	return s[:i], i
}

func isNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.'
}

// looseObject reads the object at the start of s in Gemma's notation and
// returns it as JSON, with how many bytes of s it took.
func looseObject(s string) (json.RawMessage, int, error) {
	var b strings.Builder
	depth := 0
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case strings.HasPrefix(s[i:], gemmaQuote):
			from := i + len(gemmaQuote)
			j := strings.Index(s[from:], gemmaQuote)
			if j < 0 {
				return nil, 0, fmt.Errorf("a string that does not end")
			}
			b.WriteString(jsonString(s[from : from+j]))
			i = from + j + len(gemmaQuote)
		case c == '"':
			j := i + 1
			for j < len(s) && s[j] != '"' {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(s) {
				return nil, 0, fmt.Errorf("a string that does not end")
			}
			b.WriteString(s[i : j+1])
			i = j + 1
		case c == '{' || c == '[':
			depth++
			b.WriteByte(c)
			i++
		case c == '}' || c == ']':
			depth--
			b.WriteByte(c)
			i++
			if depth == 0 {
				out := json.RawMessage(b.String())
				if !json.Valid(out) {
					return nil, 0, fmt.Errorf("not an object: %s", clip(b.String(), 80))
				}
				return out, i, nil
			}
		case strings.IndexByte(":, \t\r\n", c) >= 0:
			b.WriteByte(c)
			i++
		default:
			j := i
			for j < len(s) && strings.IndexByte("{}[]:,\" \t\r\n", s[j]) < 0 && !strings.HasPrefix(s[j:], gemmaQuote) {
				j++
			}
			word := s[i:j]
			if word == "true" || word == "false" || word == "null" ||
				(word[0] == '-' || word[0] >= '0' && word[0] <= '9') && json.Valid([]byte(word)) {
				b.WriteString(word)
			} else {
				b.WriteString(jsonString(word))
			}
			i = j
		}
		if depth == 0 {
			return nil, 0, fmt.Errorf("no object")
		}
	}
	return nil, 0, fmt.Errorf("an object that does not end")
}

// jsonString is s as a JSON string, with <, > and & left as they are.
func jsonString(s string) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

// heldCalls passes the words of a person's turn to the screen as they
// stream, and stops at the first of Gemma's call tokens: what follows is
// a call written as text, which is not for the balloon. A tail that could
// be the start of one is held until it is known not to be.
type heldCalls struct {
	out     func(string)
	buf     string
	stopped bool
}

func (h *heldCalls) delta(d string) {
	if h.stopped {
		return
	}
	h.buf += d
	cut := -1
	for _, tok := range []string{gemmaCallOpen, gemmaCallClose} {
		if i := strings.Index(h.buf, tok); i >= 0 && (cut < 0 || i < cut) {
			cut = i
		}
	}
	if cut >= 0 {
		h.out(h.buf[:cut])
		h.buf, h.stopped = "", true
		return
	}
	keep := 0
	for _, tok := range []string{gemmaCallOpen, gemmaCallClose} {
		for k := min(len(tok)-1, len(h.buf)); k > keep; k-- {
			if strings.HasSuffix(h.buf, tok[:k]) {
				keep = k
				break
			}
		}
	}
	h.out(h.buf[:len(h.buf)-keep])
	h.buf = h.buf[len(h.buf)-keep:]
}

// flush passes on a held tail once the stream has ended without a call.
func (h *heldCalls) flush() {
	if !h.stopped && h.buf != "" {
		h.out(h.buf)
	}
	h.buf = ""
}
