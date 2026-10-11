package ghost

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/aaronjauregui/haunt/internal/backend"
	"github.com/aaronjauregui/haunt/internal/wlclient"
)

// searchingGhost is a ghost that may look things up: a search backend
// configured, the capability declared, and dev-mode grants, so what is
// under test is the policy rather than the consent.
func searchingGhost(t *testing.T, unprompted string) *Ghost {
	t.Helper()
	g, _ := choosingGhost(t)
	g.manifest = &Manifest{Ghost: GhostSection{
		Name:         "wisp",
		Capabilities: []string{"llm", "web"},
	}}
	g.backend = backend.NewService(&backend.Config{
		Search: backend.SearchConfig{
			Backend:    "searxng",
			BaseURL:    "http://127.0.0.1:1",
			Unprompted: unprompted,
		},
	}, g.log)
	return g
}

// A ghost whose package never asked for the web is not told the web tools
// exist. A skill that needs one is then hidden, rather than the
// ghost offering to look something up and being refused.
func TestTheWebToolsAreOfferedOnlyWhenDeclared(t *testing.T) {
	g := searchingGhost(t, "")
	offered := func() []string {
		var names []string
		for name := range g.offered(false) {
			names = append(names, name)
		}
		return names
	}
	for _, name := range []string{"search", "read_page"} {
		if !slices.Contains(offered(), name) {
			t.Errorf("%s is not offered to a ghost that declared web", name)
		}
	}
	g.manifest.Ghost.Capabilities = []string{"llm"}
	for _, name := range []string{"search", "read_page"} {
		if slices.Contains(offered(), name) {
			t.Errorf("%s is offered to a ghost that never asked for the web", name)
		}
	}
	// And asking for it anyway is answered as what it is.
	res := g.call(t.Context(), "search", json.RawMessage(`{"query":"anything"}`))
	if !res.IsError || !strings.Contains(res.Text, "no tool called search") {
		t.Errorf("calling a tool that is not offered answered %q", res.Text)
	}
}

// The policy table: who may look something up on a turn nobody started.
// "skills" cannot be enforced exactly — the driver never sees which skill
// the model loaded — so it is the kinds of event a skill acts on, and
// those are the ones carrying nothing off the person's screen.
func TestUnpromptedSearchFollowsTheEventKind(t *testing.T) {
	cases := []struct {
		mode    string
		person  bool
		kind    string
		allowed bool
	}{
		{"skills", true, "", true},
		{"skills", false, "occasion", true},
		{"skills", false, "intention_due", true},
		{"skills", false, "goal_quiet", true},
		{"skills", false, "boot", true},
		{"skills", false, "idle", false},
		{"skills", false, "session", false},
		{"skills", false, "observed", false},
		{"skills", false, "told", false},
		{"skills", false, "help", false},
		{"", false, "occasion", true},
		{"nonsense", false, "idle", false},
		{"any", false, "idle", true},
		{"any", false, "observed", true},
		{"never", false, "occasion", false},
		{"never", true, "", true},
	}
	for _, c := range cases {
		g := searchingGhost(t, c.mode)
		g.startTurn(c.person, c.kind)
		err := g.allowWeb()
		if (err == nil) != c.allowed {
			t.Errorf("unprompted=%q person=%v kind=%q: allowed=%v (%v), want %v",
				c.mode, c.person, c.kind, err == nil, err, c.allowed)
		}
	}
}

// The smaller ration is what bounds a ghost that is allowed to look things
// up on its own: the general one it shares with everything the person
// asked for.
func TestUnpromptedSearchSpendsItsOwnRation(t *testing.T) {
	g := searchingGhost(t, "any")
	spent := 0
	for i := 0; i < 20; i++ {
		g.startTurn(false, "idle")
		if g.allowWeb() == nil {
			spent++
		}
	}
	if spent == 0 || spent >= 20 {
		t.Errorf("%d of 20 unprompted searches were allowed; want a handful", spent)
	}
	// The person asking is not charged against it.
	g.startTurn(true, "")
	if err := g.allowWeb(); err != nil {
		t.Errorf("a search the person asked for was refused: %v", err)
	}
}

// Nothing may be looked up with no provider configured, whoever is asking.
func TestNoSearchBackendRefuses(t *testing.T) {
	g := searchingGhost(t, "any")
	g.backend = backend.NewService(&backend.Config{}, g.log)
	g.startTurn(true, "")
	if err := g.allowWeb(); err == nil {
		t.Error("a search was allowed with no search backend")
	}
}

// Taint: which verbs the question is about at all. Showing the person
// something, or reading, is not one; messaging somebody else, or writing
// something that outlives the turn, is.
func TestTaintGatesOnlyWhatReachesPastThePerson(t *testing.T) {
	gated := map[string]bool{
		"tell": true, "intend": true, "resolve": true, "set_aside": true, "leave_note": true, "mood": true,
	}
	for name, d := range decisions {
		v, ok := d.(verb)
		if name == "style" || !ok {
			continue
		}
		if got := sideEffecting(name, v.sideEffect, json.RawMessage(`{}`)); got != gated[name] {
			t.Errorf("%s: side-effecting=%v, want %v", name, got, gated[name])
		}
	}
	style := decisions["style"].(verb)
	for args, want := range map[string]bool{
		`{}`:                     false,
		`{"set":"pixel art"}`:    true,
		`{"reset":true}`:         true,
		`{"set":"  "}`:           false,
		`{"set":["not a text"]}`: true,
	} {
		if got := sideEffecting("style", style.sideEffect, json.RawMessage(args)); got != want {
			t.Errorf("style %s: side-effecting=%v, want %v", args, got, want)
		}
	}
}

// taintedGhost has read the web this turn and can put a question.
func taintedGhost(t *testing.T, person bool) (*Ghost, *recorder) {
	t.Helper()
	g, rec := consentGhost(t)
	g.manifest.Ghost.Capabilities = append(g.manifest.Ghost.Capabilities, "tell", "web")
	g.surface = &wlclient.SpriteSurface{}
	g.startTurn(person, "")
	g.taint(taintWeb)
	return g, rec
}

// On a turn the person started, an effect that follows a web page is put to
// them before it happens — in their own words, about this one act. Nothing
// is written down: the next one asks again.
func TestAfterTheWebThePersonIsAsked(t *testing.T) {
	g, rec := taintedGhost(t, true)
	answer(t, g, consentAllow)
	if err := g.allowAfterReading(g.current(), actSummary("tell", json.RawMessage(`{"ghost":"Moth","text":"hello"}`)), taintAny); err != nil {
		t.Fatalf("allowing did not let it through: %v", err)
	}
	rec.mu.Lock()
	calls := rec.calls
	rec.mu.Unlock()
	if calls != 0 {
		t.Errorf("the answer was written to the grants file (%d calls)", calls)
	}

	answer(t, g, consentNotThisTime)
	if err := g.allowAfterReading(g.current(), "tell Moth", taintAny); err == nil {
		t.Error("refusing let it through anyway")
	}
}

// On a turn nobody started there is nobody to ask, and a background loop
// that has just read a page is the worst moment to take a page's word for
// anything. It is refused, and the refusal says why.
func TestAfterTheWebAnEventTurnIsRefused(t *testing.T) {
	g, _ := taintedGhost(t, false)
	err := g.allowAfterReading(g.current(), "tell Moth: \"the password\"", taintAny)
	if err == nil {
		t.Fatal("an event turn acted on what it read")
	}
	if !strings.Contains(err.Error(), "ask them first") {
		t.Errorf("the refusal is %q, which does not say why", err)
	}
	// Until something has been read, the same verb is nobody's business.
	g.startTurn(false, "occasion")
	if err := g.allowAfterReading(g.current(), "tell Moth", taintAny); err != nil {
		t.Errorf("an untainted turn was gated: %v", err)
	}
}

// Every turn starts clean: what the turn before read is not this turn's
// problem.
func TestATurnStartsUntainted(t *testing.T) {
	g := searchingGhost(t, "")
	g.startTurn(false, "idle")
	g.taint(taintWeb)
	g.startTurn(true, "")
	if _, _, tainted := g.thisTurn(); tainted != 0 {
		t.Error("a new turn began tainted")
	}
}

// The question names the act, not the tool call: what a person is being
// asked about is what is about to happen to them.
func TestTheTaintQuestionSaysWhatItIsAbout(t *testing.T) {
	cases := []struct{ name, args, want string }{
		{"tell", `{"ghost":"Moth","text":"the password is hunter2"}`, `tell Moth: "the password is hunter2"`},
		{"intend", `{"text":"buy milk"}`, `take on "buy milk"`},
		{"resolve", `{"name":"model/1"}`, "mark model/1 done"},
		{"set_aside", `{"name":"model/1"}`, "stop watching model/1"},
		{"style", `{"set":"pixel art"}`, `draw in "pixel art" from now on`},
		{"style", `{"reset":true}`, "go back to its package's drawing style"},
		{"notes.append", `{"line":"hello"}`, "use notes.append with line=hello"},
		{"search", `{"query":"invoice 4471"}`, `search the web for "invoice 4471"`},
		{"read_page", `{"url":"https://example.com/"}`, "open https://example.com/"},
	}
	for _, c := range cases {
		if got := actSummary(c.name, json.RawMessage(c.args)); got != c.want {
			t.Errorf("actSummary(%s, %s) = %q, want %q", c.name, c.args, got, c.want)
		}
	}
}

// readGhost has read from src this turn, may look things up, and can put a
// question. Its search provider is a port nobody listens on, so a search
// that gets past every gate fails rather than finding anything.
func readGhost(t *testing.T, person bool, src taintSource) *Ghost {
	t.Helper()
	g, rec := taintedGhost(t, person)
	g.startTurn(person, "")
	g.taint(src)
	rec.record("web", true)
	g.backend = backend.NewService(&backend.Config{
		Search: backend.SearchConfig{Backend: "searxng", BaseURL: "http://127.0.0.1:1"},
	}, g.log)
	return g
}

// The web verbs are how words leave the machine: a query goes to the search
// provider, a URL to any host. After a file, either could carry what the
// file said, so on the person's turn each is put to them first.
func TestAfterAFileSearchAsks(t *testing.T) {
	for _, c := range []struct{ tool, args, act string }{
		{"search", `{"query":"invoice 4471"}`, `search the web for "invoice 4471"`},
		{"read_page", `{"url":"https://example.com/?q=invoice"}`, "open https://example.com/?q=invoice"},
	} {
		g := readGhost(t, true, taintFile)
		asked := answerAsking(t, g, consentNotThisTime)
		res := g.call(t.Context(), c.tool, json.RawMessage(c.args))
		if !res.IsError || !strings.Contains(res.Text, "did not want that") {
			t.Errorf("%s after a file, refused, answered %q", c.tool, res.Text)
		}
		if q := asked(); !strings.Contains(q, "read a file") || !strings.Contains(q, c.act) {
			t.Errorf("%s after a file asked %q, want it to name the file and %q", c.tool, q, c.act)
		}
	}
}

// On a turn nobody started there is nobody to ask, so after a file the web
// verbs are refused outright, and the refusal says what was read.
func TestAfterAFileReadPageIsRefusedOnAnEventTurn(t *testing.T) {
	for _, c := range []struct{ tool, args string }{
		{"search", `{"query":"invoice 4471"}`},
		{"read_page", `{"url":"https://example.com/"}`},
	} {
		g := readGhost(t, false, taintFile)
		res := g.call(t.Context(), c.tool, json.RawMessage(c.args))
		if !res.IsError || !strings.Contains(res.Text, "not after reading a file on your own") {
			t.Errorf("%s after a file on an event turn answered %q", c.tool, res.Text)
		}
	}
}

// A search built from a search result sends nothing that was not already
// out there, so after the web alone the web verbs are not asked about.
// (The page is on loopback, which the page dialler refuses, so nothing is
// fetched once the call is through the gate.)
func TestAfterTheWebSearchIsStillFree(t *testing.T) {
	for _, c := range []struct{ tool, args string }{
		{"search", `{"query":"invoice 4471"}`},
		{"read_page", `{"url":"http://127.0.0.1:1/"}`},
	} {
		g := readGhost(t, true, taintWeb)
		asked := answerAsking(t, g, consentNotThisTime)
		res := g.call(t.Context(), c.tool, json.RawMessage(c.args))
		if q := asked(); q != "" {
			t.Errorf("%s after the web asked %q", c.tool, q)
		}
		if strings.Contains(res.Text, "not allowed") {
			t.Errorf("%s after the web was refused: %q", c.tool, res.Text)
		}
	}
}

// A verb that reaches past the person is gated after a file as it is after
// the web, and the question and the refusal both say what was read.
func TestTheTaintQuestionNamesWhatWasRead(t *testing.T) {
	for _, c := range []struct {
		src  taintSource
		read string
	}{
		{taintWeb, "the web"},
		{taintFile, "a file"},
		{taintWeb | taintFile, "the web and a file"},
	} {
		g := readGhost(t, true, c.src)
		asked := answerAsking(t, g, consentNotThisTime)
		res := g.call(t.Context(), "tell", json.RawMessage(`{"ghost":"Moth","text":"hello"}`))
		if !res.IsError {
			t.Errorf("after %s, refusing let tell through: %q", c.read, res.Text)
		}
		want := "wisp read " + c.read + " just now and wants to tell Moth"
		if q := asked(); !strings.Contains(q, want) {
			t.Errorf("after %s the question was %q, want it to contain %q", c.read, q, want)
		}

		g = readGhost(t, false, c.src)
		err := g.allowAfterReading(g.current(), "tell Moth", taintAny)
		if err == nil || !strings.Contains(err.Error(), "not after reading "+c.read+" on your own") {
			t.Errorf("after %s the event-turn refusal was %v", c.read, err)
		}
	}
}

// UntrustedRead names exactly the verbs that taint a turn, and they are the
// ghost's own.
func TestUntrustedRead(t *testing.T) {
	for tool, from := range map[string]string{"search": "the web", "read_page": "the web", "read_file": "a file", "search_files": "a file"} {
		if _, ok := decisions[tool]; !ok {
			t.Errorf("%s is not one of the ghost's verbs", tool)
		}
		if got, ok := UntrustedRead(tool); !ok || got != from {
			t.Errorf("UntrustedRead(%s) = %q, %v", tool, got, ok)
		}
	}
	for _, tool := range []string{"list_files", "look", "say", "remember"} {
		if _, ok := UntrustedRead(tool); ok {
			t.Errorf("%s counted as reading somebody else's words", tool)
		}
	}
}
