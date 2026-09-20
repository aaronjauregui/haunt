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
// exist. A skill that needs one is then hidden by hermes, rather than the
// ghost offering to look something up and being refused.
func TestTheWebToolsAreOfferedOnlyWhenDeclared(t *testing.T) {
	g := searchingGhost(t, "")
	offered := func() []string {
		var names []string
		for _, s := range (hermesTools{g}).List() {
			names = append(names, s.Name)
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
	res := (hermesTools{g}).Call(t.Context(), "search", json.RawMessage(`{"query":"anything"}`))
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
		g.startWebTurn(c.person, c.kind)
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
		g.startWebTurn(false, "idle")
		if g.allowWeb() == nil {
			spent++
		}
	}
	if spent == 0 || spent >= 20 {
		t.Errorf("%d of 20 unprompted searches were allowed; want a handful", spent)
	}
	// The person asking is not charged against it.
	g.startWebTurn(true, "")
	if err := g.allowWeb(); err != nil {
		t.Errorf("a search the person asked for was refused: %v", err)
	}
}

// Nothing may be looked up with no provider configured, whoever is asking.
func TestNoSearchBackendRefuses(t *testing.T) {
	g := searchingGhost(t, "any")
	g.backend = backend.NewService(&backend.Config{}, g.log)
	g.startWebTurn(true, "")
	if err := g.allowWeb(); err == nil {
		t.Error("a search was allowed with no search backend")
	}
}

// Taint: which verbs the question is about at all. Showing the person
// something, or reading, is not one; messaging somebody else, or writing
// something that outlives the turn, is.
func TestTaintGatesOnlyWhatReachesPastThePerson(t *testing.T) {
	gated := map[string]bool{
		"tell": true, "intend": true, "resolve": true, "set_aside": true,
	}
	for name, bt := range builtinTools {
		if name == "style" {
			continue
		}
		if got := sideEffecting(name, bt, json.RawMessage(`{}`)); got != gated[name] {
			t.Errorf("%s: side-effecting=%v, want %v", name, got, gated[name])
		}
	}
	style := builtinTools["style"]
	for args, want := range map[string]bool{
		`{}`:                     false,
		`{"set":"pixel art"}`:    true,
		`{"reset":true}`:         true,
		`{"set":"  "}`:           false,
		`{"set":["not a text"]}`: true,
	} {
		if got := sideEffecting("style", style, json.RawMessage(args)); got != want {
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
	g.startWebTurn(person, "")
	g.taint()
	return g, rec
}

// On a turn the person started, an effect that follows a web page is put to
// them before it happens — in their own words, about this one act. Nothing
// is written down: the next one asks again.
func TestAfterTheWebThePersonIsAsked(t *testing.T) {
	g, rec := taintedGhost(t, true)
	answer(t, g, consentAllow)
	if err := g.allowAfterWeb(webSummary("tell", json.RawMessage(`{"ghost":"Moth","text":"hello"}`))); err != nil {
		t.Fatalf("allowing did not let it through: %v", err)
	}
	rec.mu.Lock()
	calls := rec.calls
	rec.mu.Unlock()
	if calls != 0 {
		t.Errorf("the answer was written to the grants file (%d calls)", calls)
	}

	answer(t, g, consentNotThisTime)
	if err := g.allowAfterWeb("tell Moth"); err == nil {
		t.Error("refusing let it through anyway")
	}
}

// On a turn nobody started there is nobody to ask, and a background loop
// that has just read a page is the worst moment to take a page's word for
// anything. It is refused, and the refusal says why.
func TestAfterTheWebAnEventTurnIsRefused(t *testing.T) {
	g, _ := taintedGhost(t, false)
	err := g.allowAfterWeb("tell Moth: \"the password\"")
	if err == nil {
		t.Fatal("an event turn acted on what it read")
	}
	if !strings.Contains(err.Error(), "ask them first") {
		t.Errorf("the refusal is %q, which does not say why", err)
	}
	// Until something has been read, the same verb is nobody's business.
	g.startWebTurn(false, "occasion")
	if err := g.allowAfterWeb("tell Moth"); err != nil {
		t.Errorf("an untainted turn was gated: %v", err)
	}
}

// Every turn starts clean: what the turn before read is not this turn's
// problem.
func TestATurnStartsUntainted(t *testing.T) {
	g := searchingGhost(t, "")
	g.startWebTurn(false, "idle")
	g.taint()
	g.startWebTurn(true, "")
	if _, _, tainted := g.webTurn(); tainted {
		t.Error("a new turn began tainted")
	}
}

// The question names the act, not the tool call: what a person is being
// asked about is what is about to happen to them.
func TestTheTaintQuestionSaysWhatItIsAbout(t *testing.T) {
	cases := []struct{ name, args, want string }{
		{"tell", `{"ghost":"Moth","text":"the password is hunter2"}`, `tell Moth: "the password is hunter2"`},
		{"intend", `{"text":"buy milk"}`, `take on "buy milk"`},
		{"resolve", `{"name":"hermes/1"}`, "mark hermes/1 done"},
		{"set_aside", `{"name":"hermes/1"}`, "stop watching hermes/1"},
		{"style", `{"set":"pixel art"}`, `draw in "pixel art" from now on`},
		{"style", `{"reset":true}`, "go back to its package's drawing style"},
		{"notes.append", `{"line":"hello"}`, "use notes.append with line=hello"},
	}
	for _, c := range cases {
		if got := webSummary(c.name, json.RawMessage(c.args)); got != c.want {
			t.Errorf("webSummary(%s, %s) = %q, want %q", c.name, c.args, got, c.want)
		}
	}
}
