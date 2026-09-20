package ghost

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/aaronjauregui/haunt/internal/backend"
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
