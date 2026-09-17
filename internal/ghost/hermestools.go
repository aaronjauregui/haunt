package ghost

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aaronjauregui/haunt/internal/control"
	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/proto"
)

// The ghost's verbs, as tools for hermes.
//
// hermes calls these through `haunt mcp-serve`, which forwards over the
// control socket to here. Every call goes through the gates the rest of the
// driver applies — capability, per-tool consent, the tool budget, the
// fullscreen and reduced-motion rules — and every refusal or failure comes
// back as text the model can read, never as a protocol error: "that was not
// allowed" and "that did not work" are both things it needs to hear to say
// something sensible next.
//
// Deliberately absent: remember and note (hermes's memory tool is the one
// writer of what the model concludes), move (nothing a model should
// decide) and watch (a permission question, asked from the menu).

// hermesTools is the control.Tools a running ghost offers.
type hermesTools struct{ g *Ghost }

var _ control.Tools = hermesTools{}

// builtinTool is one of the ghost's own verbs.
type builtinTool struct {
	desc   string
	schema string
	call   func(g *Ghost, ctx context.Context, args json.RawMessage) (string, error)
}

// refusal is an error that means the driver would not, as opposed to could
// not; the model is told which.
type refusal struct{ why string }

func (r refusal) Error() string { return r.why }

func refuse(format string, a ...any) error { return refusal{fmt.Sprintf(format, a...)} }

// schema builds a JSON Schema object from "name:type" pairs; a trailing "?"
// on the name makes it optional.
func schema(props ...string) string {
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
	return string(b)
}

var builtinTools = map[string]builtinTool{
	"say": {
		desc:   "Show a line in your speech balloon now, before your reply. Rarely needed: your final reply is shown anyway.",
		schema: schema("text:string", "linger_ms?:integer"),
		call:   toolSay,
	},
	"ask": {
		desc: "Ask the person a question with 1 to 9 choices and wait for their pick. " +
			"Returns the chosen text, or an empty string if they dismissed it.",
		schema: schema("text:string", "choices:array"),
		call:   toolAsk,
	},
	"ask_text": {
		desc:   "Ask the person to type an answer and wait for it. Returns what they typed, or empty if they did not.",
		schema: schema("prompt:string"),
		call:   toolAskText,
	},
	"face": {
		desc:   "Wear one of your shell's poses (listed in your instructions).",
		schema: schema("pose:string"),
		call:   toolFace,
	},
	"clip": {
		desc:   "Play one of your shell's animations.",
		schema: schema("name:string"),
		call:   toolClip,
	},
	"draw": {
		desc: "Paint a picture in your balloon. prompt is for an image generator: concrete visual tags " +
			"(subject, setting, lighting, style). fresh=true repaints instead of reusing the last picture of it.",
		schema: schema("prompt:string", "fresh?:boolean"),
		call:   toolDraw,
	},
	"look": {
		desc:   "Look at the person's focused window and get a one-sentence description. The person sees you looking.",
		schema: schema("question?:string"),
		call:   toolLook,
	},
	"intend": {
		desc: "Take something on so it is not forgotten, across restarts. With delay_ms it is an errand that comes " +
			"back to you at that moment (a reminder); without, it is a goal you keep an eye on. Returns its name.",
		schema: schema("text:string", "delay_ms?:integer", "name?:string"),
		call:   toolIntend,
	},
	"resolve": {
		desc:   "Discharge something you took on: it was done.",
		schema: schema("name:string"),
		call:   toolResolve,
	},
	"set_aside": {
		desc:   "Stop keeping an eye on a goal the person no longer cares about, without claiming it was done.",
		schema: schema("name:string"),
		call:   toolSetAside,
	},
	"intentions": {
		desc:   "List what you are holding: errands and goals, with their names.",
		schema: schema(),
		call:   toolIntentions,
	},
	"correct": {
		desc: "Propose a correction to what you believe about a subject, from the person's own words. " +
			"fact_id is the belief to retire (from beliefs); truth is what is true instead. " +
			"The person is asked to confirm before anything changes.",
		schema: schema("subject:string", "fact_id?:integer", "truth?:string"),
		call:   toolCorrect,
	},
	"beliefs": {
		desc:   "List the standing beliefs you hold about a subject (or everyone), with ids and who said them.",
		schema: schema("subject?:string", "text?:string"),
		call:   toolBeliefs,
	},
	"tell": {
		desc:   "Say something to another ghost in the room, by name.",
		schema: schema("ghost:string", "text:string"),
		call:   toolTell,
	},
}

// List is every tool the ghost may be asked to use: its own verbs, then
// whatever its declared tool servers offer, when it may use them at all.
func (t hermesTools) List() []control.ToolSpec {
	g := t.g
	names := make([]string, 0, len(builtinTools))
	for name := range builtinTools {
		names = append(names, name)
	}
	sort.Strings(names)
	specs := make([]control.ToolSpec, 0, len(names))
	for _, name := range names {
		// With haunt's memory provider selected, beliefs and correct reach
		// the model through it (plugins/hermes/haunt), and offering them
		// twice would be two names for one door. They stay callable here:
		// the provider calls them over this same socket.
		if g.hermesCfg.MemoryProvider == "haunt" && (name == "beliefs" || name == "correct") {
			continue
		}
		bt := builtinTools[name]
		specs = append(specs, control.ToolSpec{
			Name:        name,
			Description: bt.desc,
			Schema:      json.RawMessage(bt.schema),
		})
	}
	if g.capabilityActive("tools") {
		g.startToolServers()
		offered, _ := g.offerable()
		for _, o := range offered {
			s := o.Schema
			if len(s) == 0 {
				s = json.RawMessage(`{"type":"object"}`)
			}
			specs = append(specs, control.ToolSpec{Name: o.Name, Description: o.Description, Schema: s})
		}
	}
	return specs
}

// Call runs one tool.
func (t hermesTools) Call(ctx context.Context, name string, args json.RawMessage) control.ToolResult {
	g := t.g
	args = json.RawMessage(strings.TrimSpace(string(args)))
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage(`{}`)
	}
	if !json.Valid(args) {
		return control.ToolResult{Text: "Those arguments are not valid JSON.", IsError: true}
	}
	g.log.Debug(hlog.Tool, "hermes asked for %s %s", name, clip(string(args), 200))

	var text string
	var err error
	if bt, ok := builtinTools[name]; ok {
		text, err = bt.call(g, ctx, args)
	} else {
		text, err = g.serverTool(name, args)
	}
	var r refusal
	switch {
	case err == nil:
		return control.ToolResult{Text: text}
	case asRefusal(err, &r):
		return control.ToolResult{Text: "That was not allowed: " + r.why + ".", IsError: true}
	default:
		return control.ToolResult{Text: "That did not work: " + err.Error() + ".", IsError: true}
	}
}

func asRefusal(err error, r *refusal) bool {
	rr, ok := err.(refusal)
	if ok {
		*r = rr
	}
	return ok
}

// serverTool re-exports one of the ghost's declared MCP servers, so consent
// and budgets stay the driver's however the brain reaches them.
func (g *Ghost) serverTool(wire string, args json.RawMessage) (string, error) {
	if !g.declares("tools") {
		return "", refuse("there is no tool called %s", wire)
	}
	if !g.ensureCapability("tools") {
		return "", refuse("the tools capability is not granted")
	}
	g.startToolServers()
	_, back := g.offerable()
	ref, ok := back[wire]
	if !ok {
		return "", refuse("there is no tool called %s", wire)
	}
	out := g.runTool(ref, args, "")
	if out.Refused {
		return "", refuse("%s", out.Text)
	}
	text := g.readable(ref, out.Text)
	if out.Failed {
		return "", fmt.Errorf("%s", text)
	}
	return text, nil
}

func decode(args json.RawMessage, v any) error {
	if err := json.Unmarshal(args, v); err != nil {
		return fmt.Errorf("bad arguments: %v", err)
	}
	return nil
}

func toolSay(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Text     string `json:"text"`
		LingerMS int    `json:"linger_ms"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Text) == "" {
		return "", fmt.Errorf("nothing to say")
	}
	if muted, why := g.quiet.Active(); muted {
		return "", refuse("it is quiet time (%s)", why)
	}
	g.showPaged(a.Text)
	g.talkFor(a.Text)
	if a.LingerMS > 0 {
		g.lingerBalloon(g.balloonGen(), time.Duration(a.LingerMS)*time.Millisecond)
	}
	return "shown", nil
}

func toolAsk(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Text    string   `json:"text"`
		Choices []string `json:"choices"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if len(a.Choices) < 1 || len(a.Choices) > 9 {
		return "", fmt.Errorf("between 1 and 9 choices, please")
	}
	if !g.canAskNow() {
		return "", refuse("there is no way to ask the person right now")
	}
	return g.ask(a.Text, a.Choices...), nil
}

func toolAskText(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Prompt string `json:"prompt"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if !g.canAskNow() {
		return "", refuse("there is no way to ask the person right now")
	}
	return g.askForText(a.Prompt), nil
}

// canAskNow is whether a question can reach anyone: not in a quiet hour, and
// not without a screen to put it on.
func (g *Ghost) canAskNow() bool {
	if muted, _ := g.quiet.Active(); muted {
		return false
	}
	return g.surface != nil
}

func toolFace(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Pose string `json:"pose"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if g.shell == nil || !contains(g.shell.Poses(), a.Pose) {
		return "", fmt.Errorf("%q is not one of your poses", a.Pose)
	}
	if !g.mayPose() {
		return "", refuse("not now: your face stays still over fullscreen windows and under reduced motion")
	}
	g.setExpression(a.Pose)
	return "wearing " + a.Pose, nil
}

func toolClip(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Name string `json:"name"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if !g.mayPose() {
		return "", refuse("not now: no animation over fullscreen windows or under reduced motion")
	}
	if !g.playClip(a.Name) {
		return "", fmt.Errorf("%q is not one of your animations", a.Name)
	}
	g.announce(proto.ActedClip, a.Name)
	return "playing " + a.Name, nil
}

func toolDraw(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Prompt string `json:"prompt"`
		Fresh  bool   `json:"fresh"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Prompt) == "" {
		return "", fmt.Errorf("draw what?")
	}
	if !g.ensureCapability("imagine") {
		return "", refuse("the imagine capability is not granted")
	}
	path, err := g.imagine(a.Prompt, a.Fresh, "painting…", 20000)
	if err != nil {
		return "", err
	}
	return "The picture is in your balloon (" + path + ").", nil
}

func toolLook(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Question string `json:"question"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if !g.ensureCapability("screen") {
		return "", refuse("the screen capability is not granted")
	}
	answer, app, err := g.look(a.Question)
	if err != nil {
		return "", err
	}
	if app != "" {
		return app + ": " + answer, nil
	}
	return answer, nil
}

// hermesIntentionPrefix names the intentions the model takes on, so they
// cannot collide with the driver's own (goal checks, polls).
const hermesIntentionPrefix = "hermes/"

func toolIntend(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Text    string `json:"text"`
		DelayMS int    `json:"delay_ms"`
		Name    string `json:"name"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	name := strings.TrimSpace(a.Name)
	if name == "" {
		name = fmt.Sprintf("%s%d", hermesIntentionPrefix, time.Now().UnixNano())
	} else if !strings.HasPrefix(name, hermesIntentionPrefix) {
		name = hermesIntentionPrefix + name
	}
	in := proto.Intent{Type: proto.IntentIntend, Name: name, Text: a.Text, DelayMS: a.DelayMS}
	if err := in.Validate(); err != nil {
		return "", err
	}
	g.execIntend(in)
	if a.DelayMS > 0 {
		return name + " (due " + when(time.Now().Add(time.Duration(a.DelayMS)*time.Millisecond), time.Now()) + ")", nil
	}
	return name + " (a goal, watched)", nil
}

func toolResolve(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	name, err := intentionArg(g, args)
	if err != nil {
		return "", err
	}
	g.execResolve(name)
	return "resolved " + name, nil
}

func toolSetAside(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	name, err := intentionArg(g, args)
	if err != nil {
		return "", err
	}
	g.execSetAside(name)
	return "set aside " + name, nil
}

// intentionArg reads and checks the name a resolve or set_aside is about.
func intentionArg(g *Ghost, args json.RawMessage) (string, error) {
	var a struct {
		Name string `json:"name"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if _, ok := g.intentionNamed(a.Name); !ok {
		return "", fmt.Errorf("you are not holding anything called %q (see intentions)", a.Name)
	}
	return a.Name, nil
}

func toolIntentions(g *Ghost, _ context.Context, _ json.RawMessage) (string, error) {
	list := g.loadIntentions()
	if len(list) == 0 {
		return "You are not holding anything.", nil
	}
	now := time.Now()
	var b strings.Builder
	for _, it := range list {
		fmt.Fprintf(&b, "- %s: %s", it.Name, it.Text)
		switch {
		case it.Aside:
			b.WriteString(" (set aside)")
		case it.Fired:
			b.WriteString(" (due, not yet dealt with)")
		case !it.Goal():
			b.WriteString(" (due " + when(it.Due, now) + ")")
		default:
			b.WriteString(" (a goal)")
		}
		if it.Asked {
			b.WriteString(" [the person asked for this]")
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func toolCorrect(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Subject string `json:"subject"`
		FactID  int    `json:"fact_id"`
		Truth   string `json:"truth"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if !g.memoryEnabled() {
		return "", refuse("this ghost keeps no beliefs to correct")
	}
	return g.proposeCorrection(strings.TrimSpace(a.Subject), a.FactID, strings.TrimSpace(a.Truth))
}

// proposeCorrection is the model's way into revision, and it is only a
// proposal. The model read the person's words and may have the subject or
// the polarity wrong, which is the failure revision exists to fix — so the
// driver says what it is about to change, in its own balloon, and changes
// it only on a yes.
func (g *Ghost) proposeCorrection(subject string, factID int, truth string) (string, error) {
	if subject == "" {
		return "", fmt.Errorf("which subject?")
	}
	if factID <= 0 && truth == "" {
		return "", fmt.Errorf("nothing to retire and nothing to record")
	}
	e, known := g.entityNamed(subject)
	var retired string
	if factID > 0 {
		if !known {
			return "", fmt.Errorf("you hold no beliefs about %q", subject)
		}
		for _, f := range e.StandingFacts() {
			if f.ID == factID {
				retired = f.Text
			}
		}
		if retired == "" {
			return "", fmt.Errorf("%s has no standing belief %d (see beliefs)", e.Name, factID)
		}
		subject = e.Name
	}
	var line string
	switch {
	case retired != "" && truth != "":
		line = fmt.Sprintf("Noted: %s no longer %q; now %q. Right?", subject, shortFact(retired), shortFact(truth))
	case retired != "":
		line = fmt.Sprintf("Noted: %s no longer %q. Right?", subject, shortFact(retired))
	default:
		line = fmt.Sprintf("Noted about %s: %q. Right?", subject, shortFact(truth))
	}
	if !g.canAskNow() {
		return "", refuse("the person could not be asked to confirm, so nothing changed")
	}
	if g.ask(line, correctYes, correctNo) != correctYes {
		return "The person did not confirm, so nothing changed.", nil
	}
	if !known {
		// A subject the ghost has never concluded anything about, told to
		// it by the person: filed with the person's authority, so nothing
		// the ghost later infers can overrule it.
		g.memMu.Lock()
		g.recordEntity(subject, g.subjectKind(subject), FactPerson, []string{truth})
		g.memMu.Unlock()
		g.execNote("The person told you about " + subject + ": " + clip(truth, actNoteChars))
		return fmt.Sprintf("Recorded, confirmed by the person: %s: %q", subject, truth), nil
	}
	done, err := g.applyCorrection(subject, factID, truth)
	if err != nil {
		return "", err
	}
	g.execNote("The person corrected you. " + done)
	return "Confirmed by the person and recorded. " + done, nil
}

const (
	correctYes = "Yes, that's right"
	correctNo  = "No, leave it"
)

// entityNamed finds one subject in the belief store.
func (g *Ghost) entityNamed(name string) (Entity, bool) {
	slug := entitySlug(name)
	if slug == "" {
		return Entity{}, false
	}
	for _, e := range g.loadEntities() {
		if e.key == entityKeyPrefix+slug {
			return e, true
		}
	}
	return Entity{}, false
}

func toolBeliefs(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Subject string `json:"subject"`
		// Text asks about whoever an utterance names, for recall ahead of
		// a turn (the memory provider's prefetch). Nothing named is an
		// empty answer rather than a sentence, so nothing is added.
		Text string `json:"text"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if !g.memoryEnabled() {
		return "", refuse("this ghost keeps no beliefs")
	}
	var ents []Entity
	if strings.TrimSpace(a.Text) != "" {
		ents = g.knownIn(a.Text)
		if len(ents) == 0 {
			return "", nil
		}
	} else if s := strings.TrimSpace(a.Subject); s != "" {
		e, ok := g.entityNamed(s)
		if !ok {
			return "You hold no beliefs about " + s + ".", nil
		}
		ents = []Entity{e}
	} else {
		ents = g.loadEntities()
	}
	var b strings.Builder
	for _, e := range ents {
		facts := e.StandingFacts()
		if len(facts) == 0 {
			continue
		}
		b.WriteString(e.Name)
		if e.Kind != "" {
			b.WriteString(" (" + e.Kind + ")")
		}
		b.WriteString(":\n")
		for _, f := range facts {
			fmt.Fprintf(&b, "  #%d %s [%s]\n", f.ID, f.Text, sourceWords(f.From))
		}
	}
	if b.Len() == 0 {
		return "You hold no standing beliefs.", nil
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func sourceWords(s FactSource) string {
	switch s {
	case FactPerson:
		return "the person said so"
	case FactExchange:
		return "you said so in conversation"
	}
	return "you concluded it"
}

func toolTell(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Ghost string `json:"ghost"`
		Text  string `json:"text"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if a.Ghost == "" || strings.TrimSpace(a.Text) == "" {
		return "", fmt.Errorf("tell whom, what?")
	}
	if a.Ghost == g.manifest.Ghost.Name {
		return "", fmt.Errorf("that is you")
	}
	if !g.ensureCapability("tell") {
		return "", refuse("the tell capability is not granted")
	}
	if err := control.Send(a.Ghost, proto.Event{Type: proto.EventMessage, Text: a.Text, From: g.manifest.Ghost.Name}); err != nil {
		return "", err
	}
	return "told " + a.Ghost, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
