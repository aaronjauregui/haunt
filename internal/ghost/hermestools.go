package ghost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aaronjauregui/haunt/internal/control"
	"github.com/aaronjauregui/haunt/internal/hermes"
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
// Deliberately absent: remember, note and anything that edits beliefs
// (hermes's memory tool is the one writer of what the model concludes, and
// a correction is an edit to it — docs/hermes-brain.md, section 13), move
// (nothing a model should decide) and watch (a permission question, asked
// from the menu).

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
			if len(o.Schema) == 0 {
				o.Schema = json.RawMessage(`{"type":"object"}`)
			}
			specs = append(specs, o)
		}
	}
	g.warnHiddenSkills(specs)
	return specs
}

// warnHiddenSkills logs the package's skills that hermes will not list,
// because they need a toolset or a tool this ghost does not offer. Most
// likely a tool from a [[tool]] server, spelled as the server spells it
// rather than as hermes does (mcp__haunt__<server>__<tool>, with dashes
// made underscores), or a server that is not running.
func (g *Ghost) warnHiddenSkills(specs []control.ToolSpec) {
	names := make([]string, len(specs))
	for i, s := range specs {
		names[i] = s.Name
	}
	unmet, err := hermes.Unmet(os.DirFS(filepath.Join(g.dir, "skills")), names)
	if err != nil {
		g.log.Warn(hlog.Brain, "reading the package's skills: %v", err)
		return
	}
	g.toolMu.Lock()
	defer g.toolMu.Unlock()
	for skill, missing := range unmet {
		line := skill + ": " + strings.Join(missing, ", ")
		if g.skillsWarned[line] {
			continue
		}
		if g.skillsWarned == nil {
			g.skillsWarned = map[string]bool{}
		}
		g.skillsWarned[line] = true
		g.log.Warn(hlog.Brain, "hermes will not list the skill %s: it needs %s, which this ghost does not offer",
			skill, strings.Join(missing, ", "))
	}
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

// pictureLinger is how long a picture the model drew stays up. The turn's
// words wait for it (see hermesBrain.run).
const pictureLinger = 20 * time.Second

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
	path, err := g.imagine(a.Prompt, a.Fresh, "painting…", int(pictureLinger/time.Millisecond))
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
