package ghost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
// (hermes memory, in whatever store the person chose, is the one writer
// of what the model concludes, and a correction is an edit to it —
// docs/dev/decisions.md), move
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
	// sideEffect marks a verb that reaches past the person in front of the
	// ghost — it messages someone, or writes something that outlives the
	// turn. Showing the person something is not a side effect; telling
	// somebody else is. Once web or file text has entered a turn, these
	// are the ones that need asking about again (web.go, taint).
	sideEffect bool
	// leaves marks a verb that sends words off the machine: a query to the
	// search provider, a URL to any host. It is gated only once a file has
	// been read, so that a file's contents cannot leave by that road;
	// after the web, what it would send is already out there (web.go,
	// gatedAfter).
	leaves bool
	// declared names a capability the package must have asked for before
	// the tool is offered at all. Most verbs are listed whatever the
	// manifest says and refused when they are used; the web and files
	// ones are not, because a skill that needs `search` is hidden when
	// `search` is not on the list, which is how a ghost with no web stops
	// claiming it can look things up (docs/dev/hermes.md).
	declared string
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
		desc: "Wear one of your shell's poses (listed in your instructions) without saying anything. " +
			"To wear one as you speak, open your reply with it in square brackets instead.",
		schema: schema("pose:string"),
		call:   toolFace,
	},
	"clip": {
		desc:   "Play one of your shell's animations.",
		schema: schema("name:string"),
		call:   toolClip,
	},
	"draw": {
		desc: "Paint a picture in your balloon. prompt is for an image generator: a concrete visual " +
			"description (subject, setting, lighting). Your own style is added unless style=false. " +
			"like=<name of a picture you painted> keeps that picture's composition and changes only " +
			"what your prompt changes. see=true also tells you, in one sentence, what came out.",
		schema: schema("prompt:string", "style?:boolean", "see?:boolean", "like?:string"),
		call:   toolDraw,
	},
	"show": {
		desc:   "Put a picture you painted before back in your balloon, by the name draw gave it.",
		schema: schema("id:string"),
		call:   toolShow,
	},
	"pictures": {
		desc:   "List the pictures you have painted, newest first, with their names.",
		schema: schema("count?:integer"),
		call:   toolPictures,
	},
	"style": {
		desc: "Your house style: the look added to every picture you paint. With no arguments it says what " +
			"it is. set changes it for good, reset goes back to your package's — both only when the person asks.",
		schema: schema("set?:string", "reset?:boolean"),
		call:   toolStyle,
		// Only a set or a reset is one; saying what the style is, is not
		// (sideEffecting in web.go).
		sideEffect: true,
	},
	"search": {
		desc: "Look something up on the web. One good query in the person's language, not five: " +
			"you get a few titles, snippets and links back, which are somebody else's words, not theirs.",
		schema:   schema("query:string", "count?:integer"),
		call:     toolSearch,
		declared: "web",
		leaves:   true,
	},
	"read_page": {
		desc: "Read one web page, when the search snippets are not enough. Returns the start of its text; " +
			"from continues where the last read stopped.",
		schema:   schema("url:string", "from?:integer"),
		call:     toolReadPage,
		declared: "web",
		leaves:   true,
	},
	// The files verbs are read-only, and none of them is a side effect or
	// leaves the machine: what they add to a turn is taint (files.go).
	"list_files": {
		desc: "See what is in the folders the person shared with you. No path lists the folders; " +
			"a path like notes/2026 lists what is in it.",
		schema:   schema("path?:string"),
		call:     toolListFiles,
		declared: "files",
	},
	"read_file": {
		desc: "Read one file from a shared folder: text, a PDF, or a picture (described to you). " +
			"Returns a piece at a time; from continues where the last read stopped. question is for pictures.",
		schema:   schema("path:string", "from?:integer", "question?:string"),
		call:     toolReadFile,
		declared: "files",
	},
	"search_files": {
		desc: "Find files by name or by words inside them, in the shared folders or under path. " +
			"Case does not matter. Each hit gives a from to pass to read_file.",
		schema:   schema("text:string", "path?:string", "names_only?:boolean"),
		call:     toolSearchFiles,
		declared: "files",
	},
	"look": {
		desc:   "Look at the person's focused window and get a one-sentence description. The person sees you looking.",
		schema: schema("question?:string"),
		call:   toolLook,
	},
	"intend": {
		desc: "Take something on so it is not forgotten, across restarts. With delay_ms it is an errand that comes " +
			"back to you at that moment (a reminder); without, it is a goal you keep an eye on. Returns its name.",
		schema:     schema("text:string", "delay_ms?:integer", "name?:string"),
		call:       toolIntend,
		sideEffect: true,
	},
	"resolve": {
		desc:       "Discharge something you took on: it was done.",
		schema:     schema("name:string"),
		call:       toolResolve,
		sideEffect: true,
	},
	"set_aside": {
		desc:       "Stop keeping an eye on a goal the person no longer cares about, without claiming it was done.",
		schema:     schema("name:string"),
		call:       toolSetAside,
		sideEffect: true,
	},
	"intentions": {
		desc:   "List what you are holding: errands and goals, with their names.",
		schema: schema(),
		call:   toolIntentions,
	},
	"tell": {
		desc:       "Say something to another ghost in the room, by name.",
		schema:     schema("ghost:string", "text:string"),
		call:       toolTell,
		sideEffect: true,
	},
}

// List is every tool the ghost may be asked to use: its own verbs, then
// whatever its declared tool servers offer, when it may use them at all.
func (t hermesTools) List() []control.ToolSpec {
	g := t.g
	names := make([]string, 0, len(builtinTools))
	for name, bt := range builtinTools {
		if bt.declared != "" && !g.declares(bt.declared) {
			continue
		}
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
		if bt.declared != "" && !g.declares(bt.declared) {
			return control.ToolResult{Text: "There is no tool called " + name + ".", IsError: true}
		}
		// The taint gate, for the ghost's own verbs: one check, here,
		// rather than a line in each of them (web.go).
		if after := gatedAfter(name, bt, args); after != 0 {
			if err := g.allowAfterReading(actSummary(name, args), after); err != nil {
				return refusalResult(err)
			}
		}
		text, err = bt.call(g, ctx, args)
	} else {
		text, err = g.serverTool(name, args)
	}
	if err == nil {
		return control.ToolResult{Text: text}
	}
	return refusalResult(err)
}

// refusalResult is how a tool's failure reaches the model: as content it
// can read and answer around, and saying whether the driver would not or
// could not.
func refusalResult(err error) control.ToolResult {
	var r refusal
	if asRefusal(err, &r) {
		return control.ToolResult{Text: "That was not allowed: " + r.why + ".", IsError: true}
	}
	return control.ToolResult{Text: "That did not work: " + err.Error() + ".", IsError: true}
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
	// Somebody else's program, doing something in the world: every one of
	// these is side-effecting by construction, so the taint gate is put
	// once here rather than guessed at per server (web.go).
	if err := g.allowAfterReading(actSummary(ref.String(), args), taintAny); err != nil {
		return "", err
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
	// A line said mid-turn may open with its pose, as a reply may.
	pose, text := hermes.CutPose(a.Text)
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("nothing to say")
	}
	g.wearTag(pose)
	g.showPaged(text)
	g.talkFor(text)
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
	if g.shell == nil || !slices.Contains(g.shell.Poses(), a.Pose) {
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

func toolDraw(g *Ghost, ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Prompt string `json:"prompt"`
		Style  *bool  `json:"style"`
		See    bool   `json:"see"`
		Like   string `json:"like"`
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
	// The style is on by default: a ghost looks like itself unless it is
	// asked for something else, and a model that forgets the argument gets
	// the house look rather than a picture out of character.
	styled := a.Style == nil || *a.Style
	// The model asks for a picture by the name it knows it by, and the
	// seed is looked up here. A seed is nineteen digits; a name is two,
	// and the gallery exists because short names are what a model can
	// repeat without getting one wrong.
	var seed int64
	if a.Like != "" {
		like, ok := g.picture(a.Like)
		if !ok {
			return "", fmt.Errorf("no picture %s; call pictures to see what you have", a.Like)
		}
		if like.Seed == 0 {
			return "", fmt.Errorf("%s was not painted from a seed, so there is nothing to keep; draw it fresh instead", a.Like)
		}
		seed = like.Seed
	}
	p, err := g.drawPicture(drawRequest{
		prompt:   a.Prompt,
		style:    styled,
		seed:     seed,
		wait:     "painting…",
		lingerMS: int(pictureLinger / time.Millisecond),
	})
	if err != nil {
		return "", err
	}
	g.execNote(fmt.Sprintf("drew %s: %s", p.ID, clip(p.Prompt, 120)))
	out := fmt.Sprintf("Drew %s: %q", p.ID, p.Prompt)
	if p.Style != "" {
		out += fmt.Sprintf(" (in your style: %s)", p.Style)
	}
	if a.Like != "" {
		out += fmt.Sprintf(" (from %s, so only what you changed has changed)", a.Like)
	}
	out += ". It is in your balloon."
	if a.See {
		out += " " + g.seePicture(ctx, p)
	}
	return out, nil
}

// seeQuestion is what the vision model is asked about a picture the ghost
// painted. Fixed, and narrow: the point is to catch a picture that did not
// come out as asked, not to have the model admire its own work.
const seeQuestion = "Describe this picture in one sentence: subject, composition, colours."

// seePicture shows the ghost what it painted.
//
// It is the vision backend, not the screen one: nothing of the person's is
// being looked at, so there is no screen grant in it — only the ration
// every look is charged against.
func (g *Ghost) seePicture(ctx context.Context, p Picture) string {
	if g.backend == nil || !g.backend.VisionEnabled() {
		return "(nothing to see it with: no vision backend)"
	}
	data, err := os.ReadFile(g.picturePath(p.ID))
	if err != nil {
		return "(it could not be looked at: " + err.Error() + ")"
	}
	seen, err := g.backend.DescribeImage(ctx, data, seeQuestion)
	if err != nil {
		g.log.Warn(hlog.Backend, "seeing %s: %v", p.ID, err)
		return "(it could not be looked at: " + err.Error() + ")"
	}
	p.Seen = seen
	if err := g.putPicture(p); err != nil {
		g.log.Warn(hlog.Memory, "keeping what %s shows: %v", p.ID, err)
	}
	return "It shows: " + seen
}

func toolShow(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if !g.ensureCapability("imagine") {
		return "", refuse("the imagine capability is not granted")
	}
	p, ok := g.picture(a.ID)
	if !ok {
		return "", fmt.Errorf("no picture %s; call pictures to see what you have", a.ID)
	}
	// Nothing is generated, so nothing is spent and the fullscreen rule
	// does not apply: showing a picture already on disk costs a balloon.
	if err := g.showPicture(p, int(pictureLinger/time.Millisecond)); err != nil {
		return "", err
	}
	g.execNote(fmt.Sprintf("showed %s again: %s", p.ID, clip(p.Prompt, 120)))
	return fmt.Sprintf("%s is in your balloon: %q.", p.ID, p.Prompt), nil
}

// galleryListDefault and galleryListMax bound what pictures answers with:
// enough to find last week's whale, not so much that the list is the turn.
const (
	galleryListDefault = 8
	galleryListMax     = 20
)

func toolPictures(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Count int `json:"count"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if a.Count <= 0 {
		a.Count = galleryListDefault
	}
	if a.Count > galleryListMax {
		a.Count = galleryListMax
	}
	return g.describeGallery(a.Count), nil
}

func toolStyle(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Set   string `json:"set"`
		Reset bool   `json:"reset"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	set := strings.TrimSpace(a.Set)
	if set != "" || a.Reset {
		// A standing change to how the ghost looks is the person's to
		// make. On a turn nobody started, the ghost would be redecorating
		// itself in an empty room — and a turn carrying somebody else's
		// words is exactly where a style it never chose would come from.
		if !g.respondingToUser() {
			return "", refuse("only when the person asks for a standing change")
		}
		if len([]rune(set)) > styleChars {
			return "", fmt.Errorf("that style is longer than %d characters", styleChars)
		}
		if strings.ContainsAny(set, "\n\r") {
			return "", fmt.Errorf("a style is one line")
		}
		var err error
		if a.Reset {
			err = g.clearStyle()
		} else {
			err = g.setStyle(set)
		}
		if err != nil {
			return "", err
		}
		if a.Reset {
			g.execNote("went back to its package's drawing style")
		} else {
			g.execNote("draws in " + clip(set, 200) + " from now on")
		}
	}
	return g.describeStyle(), nil
}

// describeStyle is the style as the model is told it: what it draws in,
// where that came from, and what its package would have it be.
func (g *Ghost) describeStyle() string {
	from := "from your package"
	if s, ok := g.styleChange(); ok {
		from = "changed on " + s.At.Format("2 January")
	}
	style := g.effectiveStyle()
	if style == "" {
		style = "(none)"
	}
	seed := g.styleSeed()
	if seed == "" {
		seed = "(none)"
	}
	return fmt.Sprintf("Your style: %q (%s). Your package's: %q.", style, from, seed)
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
