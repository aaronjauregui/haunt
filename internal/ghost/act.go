package ghost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aaronjauregui/haunt/internal/control"
	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/mcp"
	"github.com/aaronjauregui/haunt/internal/proto"
)

// Doing things.
//
// A ghost that can only talk is a chat window with a face. What makes this
// one different from a terminal agent is not the tools — those are
// somebody else's MCP servers, and haunt is a small client for them — but
// where it stands: in the room, all day, with a face that can show what it
// is doing and no hurry about when it does it.
//
// The whole design constraint is one sentence: **acting must be visibly
// different from talking.** An ambient process that quietly takes actions
// is a horror; an ambient character that visibly does things is a
// companion. So every tool call, however it was decided on, wears the same
// three marks — a face while it works, a line in the balloon about what it
// did, and an entry in the journal — and none of the three are optional or
// configurable, because the moment they are, the first thing anybody turns
// off is the part that made it honest.
//
// The waiting indicator goes up too, while the tool runs. It is not one of
// the three: it defers to a balloon already on screen, so a model that said
// something before it acted keeps its own line — which is exactly why the
// account afterwards has to be the unconditional one.
//
// Two things decide to act, and both come through here:
//
//   - The model, during a hermes turn. The driver re-exports the approved
//     tools through its own MCP server and runs what hermes asks for (see
//     serverTool in hermestools.go). This is what makes a persona-only
//     ghost — art, a persona, no code anywhere — able to do things.
//   - A brain, through the `act` intent, when it wants a specific thing
//     done at a specific moment rather than whatever the model decides.
//
// A third caller reads rather than acts: a `[[trigger]]` with a tool on it,
// which the driver calls on a schedule nobody is watching (poll.go). It
// comes through the same permission and the same allowance, and it is the
// one call that leaves none of the three marks — the reasons are on
// toolCall.scheduled, and they are about the marks being for acting rather
// than about polls being exempt from being honest.
//
// Neither of them holds a credential or spawns a process. The driver runs
// the servers, exactly as it holds the API keys, and for the same reason: a
// brain is a sandboxed WASM module that a person downloaded.
//
// Permission is asked twice, deliberately. The `tools` capability is the
// standing question — may this ghost act at all — and each individual tool
// is asked about the first time it is really about to run, with the
// server's own description of it in the question. A server that offers
// "read a note" and "delete the note" is one line in a manifest and two
// entirely different things to be asked.

// toolRef names one tool: the server as the manifest called it, and the
// tool as the server calls itself. Its string form, "notes.append", is what
// the user is asked about, what `haunt perms` prints, what the grants file
// records and what a brain puts in an act intent — one spelling everywhere
// a person might see it.
type toolRef struct {
	Server string
	Tool   string
}

func (r toolRef) String() string { return r.Server + "." + r.Tool }

// parseToolRef splits the "server.tool" form, validating it the same way
// the intent schema does.
func parseToolRef(name string) (toolRef, error) {
	if err := proto.ValidToolName(name); err != nil {
		return toolRef{}, err
	}
	server, tool, _ := strings.Cut(name, ".")
	return toolRef{Server: server, Tool: tool}, nil
}

// grantKey is what a decision about this tool is filed under.
func (r toolRef) grantKey() string { return toolGrantPrefix + r.String() }

// ToolGrantServer reports whether a grant key names a tool rather than a
// capability, and which server it belongs to. It is exported because the
// grants file is shared with `haunt perms`, which has to tell the two kinds
// of answer apart in order to print either of them sensibly.
func ToolGrantServer(key string) (string, bool) {
	rest, ok := strings.CutPrefix(key, toolGrantPrefix)
	if !ok {
		return "", false
	}
	ref, err := parseToolRef(rest)
	if err != nil {
		return "", false
	}
	return ref.Server, true
}

// toolGrantPrefix keeps tool decisions out of the capability namespace in
// the grants file: `llm` and `tool:notes.append` are different kinds of
// answer, and a capability list that quietly contained tool names would be
// a list nobody could read.
const toolGrantPrefix = "tool:"

// caller is the half of a tool server this file uses. It is an interface
// rather than *mcp.Server for the ordinary reason — the tests can then
// answer a call without starting a process — and because nothing here
// depends on any other part of the protocol.
type caller interface {
	Call(ctx context.Context, tool string, args json.RawMessage) (mcp.Result, error)
}

// toolEntry is one tool the ghost may use, and the server behind it.
type toolEntry struct {
	ref    toolRef
	server caller
	tool   mcp.Tool
}

// startToolServers launches every tool server the manifest declares, in
// parallel, and builds the catalogue of what they offer. Idempotent: it is
// called at boot when the capability is already granted, and again the
// moment somebody grants it, because a permission that needs a restart to
// take effect is a permission that will be granted once and then resented.
//
// Failures are logged and dropped rather than fatal. A tool server is
// somebody else's program, usually not shipped with the ghost, and a ghost
// that will not start because a note-taking helper is missing is a worse
// answer than a ghost that starts without it.
func (g *Ghost) startToolServers() {
	if len(g.manifest.Tools) == 0 || !g.capabilityActive("tools") {
		return
	}
	g.toolMu.Lock()
	defer g.toolMu.Unlock()
	if g.toolsUp {
		return
	}
	g.toolsUp = true
	g.toolCat = map[string]toolEntry{}

	type started struct {
		sec ToolSection
		srv *mcp.Server
		err error
	}
	results := make(chan started, len(g.manifest.Tools))
	for _, sec := range g.manifest.Tools {
		go func(sec ToolSection) {
			// Bounded: a server that hangs in its handshake must not hold
			// up the ghost's boot behind it.
			ctx, cancel := context.WithTimeout(g.runCtx, toolStartTimeout)
			defer cancel()
			srv, err := mcp.Start(ctx, sec.Name, sec.Command, g.toolEnv(sec), g.dir)
			results <- started{sec: sec, srv: srv, err: err}
		}(sec)
	}
	for range g.manifest.Tools {
		r := <-results
		if r.err != nil {
			g.log.Warn(hlog.Tool, "%s: %v", r.sec.Name, r.err)
			continue
		}
		g.servers = append(g.servers, r.srv)
		allowed := 0
		for _, t := range r.srv.Tools() {
			if !toolAllowed(r.sec, t.Name) {
				continue
			}
			ref := toolRef{Server: r.sec.Name, Tool: t.Name}
			g.toolCat[ref.String()] = toolEntry{ref: ref, server: r.srv, tool: t}
			allowed++
		}
		g.log.Info(hlog.Tool, "%s is up with %d of %d tools available",
			r.sec.Name, allowed, len(r.srv.Tools()))
	}
}

// toolStartTimeout bounds one server's launch and handshake. Generous for a
// local process and short enough that a broken one costs a few seconds of
// startup rather than a ghost that never appears.
const toolStartTimeout = 5 * time.Second

// stopToolServers shuts the servers down with the ghost.
func (g *Ghost) stopToolServers() {
	g.toolMu.Lock()
	servers := g.servers
	g.servers = nil
	g.toolCat = nil
	g.toolsUp = false
	g.toolMu.Unlock()
	for _, s := range servers {
		s.Close()
	}
}

// toolEnv builds the environment one server runs in: the driver's own,
// filtered to what the manifest named. Named and not valued, so a package
// can ask for a credential and can never carry one.
//
// A variable the manifest names and the user has not set is left out
// rather than passed empty: an empty token is a confusing failure inside
// somebody else's server, and its absence is a clear one.
func (g *Ghost) toolEnv(sec ToolSection) []string {
	// PATH, HOME and the rest of the base environment, because a server is
	// an ordinary program and expects to be able to find things. What is
	// filtered is the *named* set: nothing the ghost asked for arrives
	// unless it is in the driver's own environment.
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"LANG=" + os.Getenv("LANG"),
	}
	for _, name := range sec.Env {
		v, ok := os.LookupEnv(name)
		if !ok {
			g.log.Warn(hlog.Tool, "%s wants %s in the environment and it is not set", sec.Name, name)
			continue
		}
		env = append(env, name+"="+v)
	}
	return env
}

// toolAllowed applies the manifest's own upper bound on a server's tools.
func toolAllowed(sec ToolSection, tool string) bool {
	if len(sec.Allow) == 0 {
		return true
	}
	for _, a := range sec.Allow {
		if a == tool {
			return true
		}
	}
	return false
}

// toolNames is every tool the ghost may act through, sorted, in the form
// everything else spells them. Handed to brains at boot, for the same
// reason the pose list is: a brain choosing what to do should not be
// guessing names at a driver that refuses the ones that do not exist.
func (g *Ghost) toolNames() []string {
	g.toolMu.Lock()
	defer g.toolMu.Unlock()
	names := make([]string, 0, len(g.toolCat))
	for name := range g.toolCat {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// lookupTool finds one tool in the catalogue.
func (g *Ghost) lookupTool(ref toolRef) (toolEntry, bool) {
	g.toolMu.Lock()
	defer g.toolMu.Unlock()
	e, ok := g.toolCat[ref.String()]
	return e, ok
}

// offerable is the catalogue rendered for a language model, together with
// the map back from the names it will use.
//
// The names differ because the two audiences do: a person reads
// "notes.append", and tool names on the model's side accept letters,
// digits, underscores and dashes and nothing else. So the wire name substitutes,
// the map carries the truth, and the translation lives in this one
// function rather than being reversed by string-splitting at the far end —
// where a server called "my.notes" would have made it ambiguous.
func (g *Ghost) offerable() ([]control.ToolSpec, map[string]toolRef) {
	g.toolMu.Lock()
	entries := make([]toolEntry, 0, len(g.toolCat))
	for _, e := range g.toolCat {
		entries = append(entries, e)
	}
	g.toolMu.Unlock()
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].ref.String() < entries[j].ref.String()
	})

	tools := make([]control.ToolSpec, 0, len(entries))
	back := make(map[string]toolRef, len(entries))
	for _, e := range entries {
		wire := wireName(e.ref)
		for n := 2; ; n++ {
			if _, taken := back[wire]; !taken {
				break
			}
			wire = fmt.Sprintf("%s_%d", wireName(e.ref), n)
		}
		back[wire] = e.ref
		tools = append(tools, control.ToolSpec{
			Name:        wire,
			Description: e.tool.Description,
			Schema:      e.tool.Schema,
		})
	}
	return tools, back
}

// wireName renders a tool reference in the character set the chat
// completions API allows.
func wireName(r toolRef) string {
	var b strings.Builder
	for _, c := range r.Server + "__" + r.Tool {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9', c == '_', c == '-':
			b.WriteRune(c)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// toolGranted reports whether this exact tool has a standing yes.
func (g *Ghost) toolGranted(ref toolRef) bool {
	if g.opts.Granted == nil {
		// Dev mode, as everywhere else: a directory being worked on is not
		// a package somebody downloaded.
		return true
	}
	key := ref.grantKey()
	return g.opts.Granted(key) || g.asked.justAllowed(key)
}

// ensureTool settles whether this tool may run now, asking if it has to.
// Blocks while the question is on screen, so it is called off the intent
// loop and outside the AI lock, like every other question.
//
// scheduled changes the words and nothing else. A poll is consented to on
// the same terms as any other tool call, and the person deserves to be told
// which of the two they are answering about: "would like to use" is about
// the thing happening now, and a manifest trigger is about a thing that
// will keep happening (see poll.go).
func (g *Ghost) ensureTool(e toolEntry, scheduled bool) bool {
	if g.toolGranted(e.ref) {
		return true
	}
	if !g.canAskAbout(e.ref.grantKey()) {
		return false
	}
	// The server's own words for what the tool does, since the driver has
	// no better description and a name alone is not something anybody can
	// consent to. Trimmed to one line: some servers write paragraphs, and
	// a balloon is not a manual.
	what := summaryLine(e.tool.Description)
	question := g.manifest.Ghost.Name + " would like to use " + e.ref.String()
	if scheduled {
		question = g.manifest.Ghost.Name + " would like to check " + e.ref.String() +
			" on a schedule, without being asked each time"
	}
	if what != "" {
		question += " — " + what
	}
	question += ". Allow?"
	return g.askPermission(e.ref.grantKey(), question,
		func() bool { return g.toolGranted(e.ref) },
		consentAllow, consentOnce, consentNotNow, consentNever)
}

// summaryLine trims a description to something that fits in a balloon
// beside a question.
func summaryLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	const most = 140
	if len(s) > most {
		s = strings.TrimSpace(s[:most]) + "…"
	}
	return s
}

// acted is what one tool call came to.
type acted struct {
	// Text is what the tool said, or why it did not run.
	Text string
	// Refused means the driver would not do it: no capability, no consent,
	// no allowance, no such tool. Distinct from a tool that ran and failed,
	// which is an answer and belongs back in the conversation.
	Refused bool
	// Failed means the tool ran and said it did not work.
	Failed bool
}

// toolCall is one thing to do, and the terms it is done on. The fields
// past the first two are the differences between the two kinds of caller —
// something the ghost is doing now, in front of somebody, and a manifest
// trigger's poll happening on its own schedule.
type toolCall struct {
	ref  toolRef
	args json.RawMessage
	// wait is what the ghost says while it works, in the character's own
	// words. Empty gets the driver's.
	wait string
	// scheduled says this is a manifest trigger's poll rather than
	// something decided just now, which changes the consent question and
	// takes the three visible marks off.
	//
	// The marks come off because they are for acting and this is reading:
	// a balloon line and a journal entry every fifteen minutes would be
	// noise the person turns off, and journal entries ride into every
	// prompt, so a calendar poll would slowly replace what the ghost
	// actually concluded with what it read. What stands in their place is
	// not nothing — the trigger is written in the manifest the person
	// installed, the tool is consented to by name in a question that says
	// it is scheduled, and every poll is logged. See poll.go.
	scheduled bool
}

// runTool is one thing done in front of somebody: permission, allowance,
// the three visible marks, and the call itself. Every path that acts goes
// through it, which is what makes "acting is visibly different from
// talking" a property of the driver rather than a habit of whichever brain
// happens to be running.
//
// It does not gate initiative; its callers do, because whether an action
// was provoked is a question about the moment the request arrived and not
// about the moment a tool server gets round to it.
func (g *Ghost) runTool(ref toolRef, args json.RawMessage, wait string) acted {
	return g.callTool(toolCall{ref: ref, args: args, wait: wait})
}

// callTool is runTool and the scheduled poll behind one set of gates.
// Permission, the allowance and the call itself are the same for both, and
// deliberately so: a poll is a tool call, spends the user's tool budget
// like one, and is refused by the same "never" as one.
func (g *Ghost) callTool(c toolCall) acted {
	ref, args := c.ref, c.args
	e, known := g.lookupTool(ref)
	if !known {
		// Distinguishing "the manifest never declared it" from "the server
		// is not running" is not worth doing here: a brain and a model both
		// need the same thing back, which is that this is not something to
		// try again.
		return acted{Text: "there is no tool called " + ref.String(), Refused: true}
	}
	if !g.ensureTool(e, c.scheduled) {
		return acted{Text: "not allowed to use " + ref.String(), Refused: true}
	}
	// The allowance is the driver's, and there is no acting without one:
	// the same rule the AI executors state, since a ghost with no service
	// behind it has no budget to spend and no business spending one.
	if g.aiSvc == nil {
		return acted{Text: "no AI service, so no allowance to act against", Refused: true}
	}
	if err := g.aiSvc.SpendToolCall(); err != nil {
		g.log.Info(hlog.Tool, "%s: %v", ref, err)
		return acted{Text: err.Error(), Refused: true}
	}

	var indicator *waiter
	if !c.scheduled {
		// The face, first of the three marks. Remembered and put back
		// afterwards, which is the one place in the driver that returns a
		// face to where it found it: a pose worn *for the duration of
		// something* has an end, unlike the ones that stand until the next
		// line.
		defer g.wearWorking()()

		// The balloon, second mark, while it works. The words are the
		// brain's when it supplied any and the driver's otherwise, exactly
		// as for a slow model.
		wait := c.wait
		if wait == "" {
			wait = "(" + ref.String() + "…)"
		}
		indicator = g.startWaiting(wait)
	}

	ctx, cancel := context.WithTimeout(g.sessionContext(), g.aiSvc.ToolTimeout())
	defer cancel()
	g.log.Info(hlog.Tool, "running %s", ref)
	res, err := e.server.Call(ctx, e.tool.Name, args)
	// Nil and a no-op for a poll, which never put one up.
	indicator.Stop()

	out := acted{}
	switch {
	case err != nil:
		g.log.Warn(hlog.Tool, "%s: %v", ref, err)
		out = acted{Text: err.Error(), Failed: true}
	default:
		out = acted{Text: res.Text, Failed: res.IsError}
		if out.Text == "" && !c.scheduled {
			// Something has to go back to whoever is waiting. A poll is
			// nobody waiting, and an empty answer is a real answer there —
			// a calendar with nothing on it — so it stays empty rather than
			// becoming a sentence for a ghost to repeat.
			out.Text = "(it did not say anything)"
		}
	}

	if c.scheduled {
		// Nothing on screen, nothing in the journal, nothing to the
		// neighbours. What happened is that the ghost read something; what
		// it does about it is where the marks belong.
		return out
	}
	// The balloon again, and this is the mark that matters: what it did,
	// after it did it, whether or not anybody was watching while it ran.
	g.noticeAct(ref, out.Failed)
	// The journal, third mark. It is also the ambient half of the promise:
	// what the ghost did is part of what it remembers, so it can be asked
	// about it a week later, and so what it has been doing shapes what it
	// says next.
	g.journalAct(ref, args, out)
	// And the room. The neighbours saw a ghost visibly working and a line
	// in its balloon, which is exactly what they are told — the name of the
	// tool, never its arguments or its answer.
	g.announce(proto.ActedUsedTool, ref.String())
	return out
}

// wearWorking puts on a working face and returns the function that takes it
// off again. Both halves are no-ops when the shell has nothing suitable or
// the moment is wrong, so a caller never has to ask.
func (g *Ghost) wearWorking() func() {
	if g.shell == nil || !g.mayPose() {
		return func() {}
	}
	g.shellMu.Lock()
	was := g.shell.Pose()
	poses := g.shell.Poses()
	g.shellMu.Unlock()

	has := map[string]bool{}
	for _, p := range poses {
		has[p] = true
	}
	// In preference order, and every one of them optional: a shell drawn
	// before any of this existed has none, and a ghost with one face is not
	// a ghost that may act invisibly — the balloon and the journal are the
	// marks that no shell can fail to show.
	var wear string
	for _, p := range []string{"working", "busy", "thinking"} {
		if has[p] {
			wear = p
			break
		}
	}
	if wear == "" || wear == was {
		return func() {}
	}
	g.setExpression(wear)
	// Back to the face it had. Not to "neutral": the ghost may have looked
	// sleepy at one in the morning for reasons of its own, and a driver
	// that tidied that away would be overruling the brain about the
	// character's face.
	return func() {
		if was != "" {
			g.setExpression(was)
		}
	}
}

// actLinger is how long the "here is what I did" line stays. Long enough to
// read a tool's name, short enough that it is gone before it is furniture —
// and short, too, because in a conversation the reply is arriving behind it.
const actLinger = 4 * time.Second

// noticeAct puts the second mark on screen: a line saying what just ran.
// The driver's own voice rather than the character's, in the parentheses it
// uses everywhere it speaks for itself — the ghost is not narrating, the
// driver is reporting.
func (g *Ghost) noticeAct(ref toolRef, failed bool) {
	line := "(" + ref.String() + " — done)"
	if failed {
		line = "(" + ref.String() + " — that didn't work)"
	}
	if g.surface == nil {
		g.log.Info(hlog.Tool, "%s", line)
		return
	}
	// showBalloon rather than showPaged: a one-line report has nothing to
	// page through, and showPaged mints an `spoke` announcement for the
	// other ghosts in the room. This line is the driver reporting, not the
	// character speaking, and what the room is told about it is the
	// used_tool announcement below.
	g.showBalloon(line)
	g.lingerBalloon(g.balloonGen(), actLinger)
}

// journalAct writes the third mark. Short on purpose: recent journal
// entries ride along in every prompt, so a verbose account of a tool call
// would crowd out the things the ghost actually chose to remember. What is
// kept is what it would want to be able to say later — what it did, roughly
// what with, and how it went.
func (g *Ghost) journalAct(ref toolRef, args json.RawMessage, out acted) {
	entry := "You used " + ref.String()
	if a := summariseArgs(args); a != "" {
		entry += " (" + a + ")"
	}
	switch {
	case out.Failed:
		entry += " and it failed: " + clip(out.Text, actNoteChars)
	default:
		entry += " and it said: " + clip(out.Text, actNoteChars)
	}
	g.execNote(entry)
}

// actNoteChars is how much of a tool's answer the journal keeps.
const actNoteChars = 160

// summariseArgs renders an argument object as "key=value, key=value",
// briefly. The journal wants enough to tell two calls apart and not the
// whole payload — a tool handed a file's contents would otherwise put the
// file in the ghost's prompt for the next twenty exchanges.
func summariseArgs(args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(args, &m) != nil {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+clip(fmt.Sprint(m[k]), actArgChars))
	}
	return clip(strings.Join(parts, ", "), actNoteChars)
}

// actArgChars is how much of one argument's value is worth keeping.
const actArgChars = 60

// clip shortens text to n characters, marking that it did.
func clip(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "…"
}

// execAct is a brain asking for one specific thing to be done. The model's
// own tool calls do not come through here — they are inside an exchange
// that has already been gated and charged (see execChat) — but everything
// past the gate is the same code.
// It runs with the AI lock held and the standing capability already
// settled; see the act case in execIntent for both.
func (g *Ghost) execAct(in proto.Intent) {
	ref, err := parseToolRef(in.Name)
	if err != nil {
		g.publishAIError(err.Error(), in.Tag)
		return
	}
	out := g.runTool(ref, in.Args, in.Wait)
	if out.Refused {
		g.publishAIError(out.Text, in.Tag)
		return
	}
	g.publish(proto.Event{
		Type: proto.EventActed,
		Name: ref.String(),
		Text: out.Text,
		Tag:  in.Tag,
	})
}
