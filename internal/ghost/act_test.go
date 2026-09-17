package ghost

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/ai"
	"github.com/aaronjauregui/haunt/internal/mcp"
	"github.com/aaronjauregui/haunt/internal/proto"
)

// fakeServer answers calls without a process on the end of a pipe. What is
// under test here is the driver's half — permission, allowance, and the
// three marks acting has to leave — and none of that is about JSON-RPC.
type fakeServer struct {
	mu    sync.Mutex
	calls []string
	res   mcp.Result
	err   error
}

func (f *fakeServer) Call(_ context.Context, tool string, args json.RawMessage) (mcp.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, tool+"("+string(args)+")")
	return f.res, f.err
}

func (f *fakeServer) ran() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// actingGhost is a ghost with one tool server, the tools capability
// granted, and nothing decided about the individual tools — which is the
// state every ghost is in the first time it is about to do something.
func actingGhost(t *testing.T) (*Ghost, *fakeServer, *recorder) {
	t.Helper()
	g, r := consentGhost(t)
	g.manifest.Ghost.Capabilities = append(g.manifest.Ghost.Capabilities, "tools")
	g.manifest.Tools = []ToolSection{{Name: "notes", Command: []string{"true"}}}
	// The standing capability is settled; what these tests are about is the
	// question that follows it, once per tool.
	r.record("tools", true)

	srv := &fakeServer{res: mcp.Result{Text: "written"}}
	g.toolsUp = true
	g.toolCat = map[string]toolEntry{
		"notes.append": {
			ref:    toolRef{Server: "notes", Tool: "append"},
			server: srv,
			tool:   mcp.Tool{Name: "append", Description: "Append a line to today's note"},
		},
	}
	// A real allowance, so the budget is the driver's own, and a store to
	// journal into.
	g.aiSvc = ai.NewService(&ai.Config{}, g.log)
	g.store = &memStore{m: map[string]string{}}
	return g, srv, r
}

func appendRef() toolRef { return toolRef{Server: "notes", Tool: "append"} }

// The first use of a tool asks about that tool, by name, with the server's
// own description of it — a name alone is not something anybody can consent
// to.
func TestFirstUseAsksAboutTheTool(t *testing.T) {
	g, srv, r := actingGhost(t)
	asked := answerAsking(t, g, consentAllow)

	out := g.runTool(appendRef(), json.RawMessage(`{"text":"hi"}`), "")
	if out.Refused {
		t.Fatalf("refused after the person allowed it: %s", out.Text)
	}
	if got := srv.ran(); len(got) != 1 || got[0] != `append({"text":"hi"})` {
		t.Errorf("ran %v, want the one call with its arguments intact", got)
	}
	if !r.granted("tool:notes.append") {
		t.Error("the answer was not written against the tool's own grant key")
	}
	question := asked()
	if !strings.Contains(question, "notes.append") {
		t.Errorf("question = %q, want the tool named in it", question)
	}
	if !strings.Contains(question, "Append a line") {
		t.Errorf("question = %q, want the server's description of what it does", question)
	}
}

// answerAsking is answer, keeping what was on screen when it answered: a
// question about acting has to name the thing being consented to, and that
// is only checkable from the balloon it was asked in.
func answerAsking(t *testing.T, g *Ghost, option string) func() string {
	t.Helper()
	var mu sync.Mutex
	var seen string
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for !g.choiceOpen() {
			if time.Now().After(deadline) {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		g.choiceMu.Lock()
		mu.Lock()
		seen = g.choice.text
		mu.Unlock()
		at := -1
		for i, o := range g.choice.options {
			if o == option {
				at = i
			}
		}
		g.choiceMu.Unlock()
		if at >= 0 {
			g.answerChoice(at)
		} else {
			g.dismissChoices()
		}
	}()
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return seen
	}
}

// "Just this once" runs the tool and writes nothing down, so the next call
// is a fresh question rather than a silent yes.
func TestJustThisOnceRecordsNothing(t *testing.T) {
	g, srv, r := actingGhost(t)
	answer(t, g, consentOnce)

	if out := g.runTool(appendRef(), nil, ""); out.Refused {
		t.Fatalf("refused after the person allowed it once: %s", out.Text)
	}
	if len(srv.ran()) != 1 {
		t.Fatalf("ran %v, want the call to have happened", srv.ran())
	}
	if n := r.decisions() - 1; n != 0 { // the one for `tools` in the setup
		t.Errorf("%d decisions were recorded; 'just this once' records none", n)
	}
	// And it is asked again rather than silently allowed or silently
	// refused: the answer applied to one call and this is another one.
	answer(t, g, consentNotNow)
	if out := g.runTool(appendRef(), nil, ""); !out.Refused {
		t.Error("the second call went ahead without being asked about")
	}
	if len(srv.ran()) != 1 {
		t.Errorf("ran %v, want the refused second call not to have happened", srv.ran())
	}
}

// A refusal is a refusal: nothing runs, and what comes back says so in a
// way the model or the brain can act on.
func TestRefusedToolDoesNotRun(t *testing.T) {
	g, srv, _ := actingGhost(t)
	answer(t, g, consentNever)

	out := g.runTool(appendRef(), nil, "")
	if !out.Refused {
		t.Fatal("a refused tool ran")
	}
	if len(srv.ran()) != 0 {
		t.Errorf("ran %v, want nothing", srv.ran())
	}
}

// The whole design constraint, as a test: a tool call leaves an entry in
// the journal, whether or not anybody was watching the balloon.
func TestActingIsWrittenDown(t *testing.T) {
	g, _, _ := actingGhost(t)
	answer(t, g, consentAllow)

	g.runTool(appendRef(), json.RawMessage(`{"text":"buy milk"}`), "")

	notes := g.loadNotes()
	if len(notes) != 1 {
		t.Fatalf("%d journal entries, want the one for the action", len(notes))
	}
	entry := notes[0].Text
	for _, want := range []string{"notes.append", "buy milk", "written"} {
		if !strings.Contains(entry, want) {
			t.Errorf("journal entry %q does not mention %q", entry, want)
		}
	}
}

// And the neighbours are told what it did — the tool's name, never its
// arguments or its answer, which are as private as the prompt behind a
// picture.
func TestActingIsAnnouncedNarrowly(t *testing.T) {
	g, _, _ := actingGhost(t)
	stage := &recordingStage{}
	g.opts.Stage = stage
	answer(t, g, consentAllow)

	g.runTool(appendRef(), json.RawMessage(`{"text":"a secret"}`), "")

	if len(stage.seen) != 1 {
		t.Fatalf("announced %v, want the one action", stage.seen)
	}
	action, detail := stage.seen[0][0], stage.seen[0][1]
	if action != proto.ActedUsedTool {
		t.Errorf("action = %q, want %q", action, proto.ActedUsedTool)
	}
	if detail != "notes.append" {
		t.Errorf("detail = %q, want just the tool's name", detail)
	}
	if strings.Contains(detail, "secret") {
		t.Error("the arguments reached the other ghosts in the room")
	}
}

// A tool that fails on its own terms is an answer and not a refusal: the
// thing that asked needs to hear it in order to say anything sensible.
func TestToolFailureIsNotARefusal(t *testing.T) {
	g, srv, _ := actingGhost(t)
	srv.res = mcp.Result{Text: "no such note", IsError: true}
	answer(t, g, consentAllow)

	out := g.runTool(appendRef(), nil, "")
	if out.Refused {
		t.Error("a tool that ran and failed was reported as a refusal")
	}
	if !out.Failed {
		t.Error("the failure was not reported as one")
	}
	if out.Text != "no such note" {
		t.Errorf("text = %q, want what the tool said", out.Text)
	}
}

// A transport failure is the same shape: the model hears about it rather
// than the exchange dying.
func TestServerErrorIsAnAnswer(t *testing.T) {
	g, srv, _ := actingGhost(t)
	srv.err = errors.New("the server stopped")
	answer(t, g, consentAllow)

	out := g.runTool(appendRef(), nil, "")
	if out.Refused || !out.Failed {
		t.Errorf("out = %+v, want a failure that is not a refusal", out)
	}
	if !strings.Contains(out.Text, "stopped") {
		t.Errorf("text = %q, want the server's error in it", out.Text)
	}
}

// A tool nobody declared is refused before any question is put: the
// manifest is the outer bound, and asking about something outside it would
// be teaching people to say yes to names they cannot check.
func TestUnknownToolIsRefusedWithoutAsking(t *testing.T) {
	g, _, r := actingGhost(t)
	before := r.decisions()
	out := g.runTool(toolRef{Server: "notes", Tool: "delete"}, nil, "")
	if !out.Refused {
		t.Fatal("a tool that is not in the catalogue ran")
	}
	if r.decisions() != before || g.choiceOpen() {
		t.Error("the driver asked about a tool the ghost does not have")
	}
}

// The manifest's own upper bound, under the user's.
func TestAllowListBoundsWhatIsOffered(t *testing.T) {
	sec := ToolSection{Name: "notes", Allow: []string{"append"}}
	if !toolAllowed(sec, "append") {
		t.Error("the allowed tool was excluded")
	}
	if toolAllowed(sec, "delete") {
		t.Error("a tool outside the allow list was offered")
	}
	// Empty means everything the server has, which is the ordinary case.
	if !toolAllowed(ToolSection{Name: "notes"}, "delete") {
		t.Error("an empty allow list excluded a tool")
	}
}

// Names round-trip: what the user reads, what the grants file records, and
// what a brain writes into an intent are all one spelling.
func TestToolNameForm(t *testing.T) {
	ref, err := parseToolRef("notes.append")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Server != "notes" || ref.Tool != "append" {
		t.Errorf("parsed %+v, want notes/append", ref)
	}
	if ref.String() != "notes.append" {
		t.Errorf("String() = %q", ref.String())
	}
	if got, ok := ToolGrantServer(ref.grantKey()); !ok || got != "notes" {
		t.Errorf("ToolGrantServer(%q) = %q, %v", ref.grantKey(), got, ok)
	}
	if _, ok := ToolGrantServer("llm"); ok {
		t.Error("a plain capability was read as a tool grant")
	}
	for _, bad := range []string{"append", "notes.", ".append", "a.b.c"} {
		if _, err := parseToolRef(bad); err == nil {
			t.Errorf("parseToolRef(%q) was accepted", bad)
		}
	}
}

// The model's names are not the user's: the chat API allows letters,
// digits, underscores and dashes, and "notes.append" is none of those in
// the middle. The map back is what keeps the translation honest.
func TestOfferableNamesAreModelSafe(t *testing.T) {
	g, srv, _ := actingGhost(t)
	g.toolCat["my.notes.search"] = toolEntry{
		ref:    toolRef{Server: "my.notes", Tool: "search"},
		server: srv,
	}
	tools, back := g.offerable()
	if len(tools) != 2 {
		t.Fatalf("offered %d tools, want both", len(tools))
	}
	for _, tool := range tools {
		for _, c := range tool.Name {
			ok := c == '_' || c == '-' ||
				(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
			if !ok {
				t.Errorf("tool name %q contains %q, which the chat API rejects", tool.Name, c)
			}
		}
		if _, found := back[tool.Name]; !found {
			t.Errorf("%q has no way back to a real tool", tool.Name)
		}
	}
}

// A model that asks for something that was never offered is told so,
// rather than the turn failing: that is the answer that stops it happening
// twice.
func TestUnofferedToolCallIsExplained(t *testing.T) {
	g, srv, _ := actingGhost(t)
	res := hermesTools{g}.Call(context.Background(), "send_email", nil)
	if !res.IsError || len(srv.ran()) != 0 {
		t.Fatal("a tool the model invented ran")
	}
	if !strings.Contains(res.Text, "send_email") {
		t.Errorf("text = %q, want the name it asked for", res.Text)
	}
}

// Bad arguments are a mistake a model can be told about and correct, not a
// reason to end the turn.
func TestBadArgumentsAreExplained(t *testing.T) {
	g, srv, _ := actingGhost(t)
	res := hermesTools{g}.Call(context.Background(), "notes__append", json.RawMessage("{not json"))
	if !res.IsError || len(srv.ran()) != 0 {
		t.Fatal("a call with unparseable arguments reached the server")
	}
	if !strings.Contains(res.Text, "JSON") {
		t.Errorf("text = %q, want it to say what was wrong", res.Text)
	}
}

// Nothing, or blank, is what a model sends when it means "no arguments",
// and is not a mistake.
func TestEmptyArgumentsMeanNone(t *testing.T) {
	g, srv, _ := actingGhost(t)
	answer(t, g, consentAllow)
	res := hermesTools{g}.Call(context.Background(), "notes__append", json.RawMessage("  "))
	if res.IsError {
		t.Fatalf("the call did not run: %s", res.Text)
	}
	if got := srv.ran(); len(got) != 1 || got[0] != "append({})" {
		t.Errorf("ran %v, want one call with no arguments", got)
	}
}

// The allowance is the driver's, not the ghost's: a tool loop cannot spend
// past it however keen the model is.
func TestToolBudgetRefuses(t *testing.T) {
	g, srv, _ := actingGhost(t)
	g.aiSvc = ai.NewService(&ai.Config{
		Budget: ai.BudgetConfig{ToolRequestsPerHour: 1},
	}, g.log)
	answer(t, g, consentAllow)

	if out := g.runTool(appendRef(), nil, ""); out.Refused {
		t.Fatalf("the first call was refused: %s", out.Text)
	}
	out := g.runTool(appendRef(), nil, "")
	if !out.Refused {
		t.Fatal("the second call went through an exhausted allowance")
	}
	if len(srv.ran()) != 1 {
		t.Errorf("ran %v, want only the affordable call", srv.ran())
	}
}

// The journal is injected into every prompt, so an action's entry has to
// stay short whatever the tool answered with.
func TestJournalEntryStaysShort(t *testing.T) {
	g, srv, _ := actingGhost(t)
	srv.res = mcp.Result{Text: strings.Repeat("verbose ", 500)}
	answer(t, g, consentAllow)

	g.runTool(appendRef(), json.RawMessage(
		`{"text":"`+strings.Repeat("x", 400)+`"}`), "")

	notes := g.loadNotes()
	if len(notes) != 1 {
		t.Fatalf("%d journal entries, want one", len(notes))
	}
	if n := len(notes[0].Text); n > maxNoteLength {
		t.Errorf("journal entry is %d characters, over the %d cap", n, maxNoteLength)
	}
}

func TestSummariseArgs(t *testing.T) {
	got := summariseArgs(json.RawMessage(`{"b":2,"a":"one"}`))
	if got != "a=one, b=2" {
		t.Errorf("summariseArgs = %q, want the keys sorted and rendered", got)
	}
	if summariseArgs(nil) != "" {
		t.Error("no arguments produced a summary of something")
	}
}

// recordingStage is the room, for the announcement test.
type recordingStage struct {
	seen [][2]string
}

func (s *recordingStage) Announce(action, detail string) {
	s.seen = append(s.seen, [2]string{action, detail})
}
func (s *recordingStage) Observe(func(from, action, detail string)) {}
func (s *recordingStage) Leave()                                    {}

// decisions is how many answers the recorder has been handed, which is what
// "nothing was written down" is checked against.
func (r *recorder) decisions() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}
