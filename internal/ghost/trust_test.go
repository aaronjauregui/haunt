package ghost

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/backend"
	"github.com/aaronjauregui/haunt/internal/config"
	"github.com/aaronjauregui/haunt/internal/model"
	"github.com/aaronjauregui/haunt/internal/wlclient"
)

// Endpoints the trust is derived from: a llama-server on this machine, and
// a provider somebody else runs.
var (
	localModel = model.Config{BaseURL: "http://127.0.0.1:8080/v1", Name: "gemma-4"}
	cloudModel = model.Config{BaseURL: "https://api.z.ai/api/paas/v4", Name: "glm-5.3"}
)

// The table is the spec's, row by row.
func TestTrustTable(t *testing.T) {
	want := map[verbClass][2]need{
		classReadAsked:   {onCloud: needConsent | needAfterRead, onLocal: 0},
		classReadUnasked: {onCloud: needConsent | needPolicy | needRation | needAfterRead, onLocal: needRation},
		classLookup:      {},
		classWrite:       {onCloud: needEcho | needLabel, onLocal: needLabel},
		classKeep:        {onCloud: needAfterRead, onLocal: 0},
		classReach:       {onCloud: needConsent | needMarks | needAfterRead, onLocal: needConsent | needMarks | needAfterRead},
		classShow:        {},
	}
	for c, w := range want {
		if got := trustTable[c]; got != w {
			t.Errorf("class %d: %v, want %v", c, got, w)
		}
	}
	for name, c := range map[string]verbClass{
		"recall": classLookup, "skill_view": classLookup,
		"remember": classWrite, "rewrite_notes": classWrite, "skill_write": classWrite,
		"intend": classKeep, "resolve": classKeep, "set_aside": classKeep, "style": classKeep,
		"tell": classReach, "notes__append": classReach,
		"speak": classShow, "face": classShow, "clip": classShow, "draw": classShow, "show": classShow, "look": classShow,
	} {
		if got := classOf(name, true); got != c {
			t.Errorf("classOf(%s) = %d, want %d", name, got, c)
		}
	}
	if classOf("read_file", true) != classReadAsked || classOf("read_file", false) != classReadUnasked {
		t.Error("a read is not classed by who started the turn")
	}
	// Every decision in the registry is classed on purpose; the default
	// is for server tools, and for the throwaway TestNewDecision adds.
	for name := range decisions {
		if _, ok := verbClasses[name]; !ok && name != "hum" {
			t.Errorf("decision %s has no class in the trust table", name)
		}
	}
	// Trust follows the endpoint, overridable.
	g, _ := choosingGhost(t)
	for cfg, w := range map[model.Config]trust{
		localModel: onLocal, cloudModel: onCloud, {}: onCloud,
		{BaseURL: cloudModel.BaseURL, Trust: model.TrustLocal}: onLocal,
		{BaseURL: localModel.BaseURL, Trust: model.TrustCloud}: onCloud,
	} {
		g.modelCfg = cfg
		if got := g.trust(); got != w {
			t.Errorf("%+v: trust %v, want %v", cfg, got, w)
		}
	}
}

// On a local model, a read on a turn nobody started needs no policy and
// no grant: it happens, and goes in the journal. Quiet hours, the ration
// and a refusal for good still stop it.
func TestTrustLocalReads(t *testing.T) {
	g, _ := readingGhost(t, "", backend.BudgetConfig{UnpromptedFilePerHour: 1}, "")
	g.modelCfg = localModel
	refused := false
	g.opts.Granted = func(string) bool { return false }
	g.opts.Refused = func(string) bool { return refused }
	g.startTurn(false, "idle")
	res := call(t, g, "read_file", `{"path":"notes/todo.md"}`)
	if res.IsError || !strings.Contains(res.Text, "plumber") {
		t.Fatalf("a local read on an idle turn answered %q", res.Text)
	}
	notes := g.loadNotes()
	if len(notes) == 0 || notes[len(notes)-1].Text != "read notes/todo.md" {
		t.Errorf("journal = %+v, want the read", notes)
	}
	if res := call(t, g, "read_file", `{"path":"notes/todo.md"}`); !res.IsError || !strings.Contains(res.Text, "budget") {
		t.Errorf("a second unprompted read on a ration of one answered %q", res.Text)
	}
	g.quiet.Away(time.Now().Add(time.Hour))
	if res := call(t, g, "list_files", `{}`); !res.IsError || !strings.Contains(res.Text, "quiet time") {
		t.Errorf("a local read in quiet hours answered %q", res.Text)
	}
	g.quiet.Home()
	// The person's own turn is free of the ration.
	g.startTurn(true, "")
	for range 3 {
		if res := call(t, g, "read_file", `{"path":"notes/todo.md"}`); res.IsError {
			t.Fatalf("a local read on the person's turn answered %q", res.Text)
		}
	}
	refused = true
	if res := call(t, g, "read_file", `{"path":"notes/todo.md"}`); !res.IsError || !strings.Contains(res.Text, "refused") {
		t.Errorf("a local read refused for good answered %q", res.Text)
	}

	// The web likewise: an idle turn gets past the policy and the grant,
	// and fails only at the provider, which nobody listens on.
	w := searchingGhost(t, "never")
	w.modelCfg = localModel
	w.opts.Granted = func(string) bool { return false }
	w.startTurn(false, "idle")
	if err := w.allowWeb(); err != nil {
		t.Errorf("a local search on an idle turn was refused: %v", err)
	}
}

// `haunt perms revoke` reaches both kinds of model. On local, where a read
// is never asked for, the withdrawal is what refuses it; on cloud the
// revoke took the grant away, the withdrawal itself is not consulted, and
// the read is asked for again as if it had never been granted.
func TestTrustWithdrawnReads(t *testing.T) {
	for _, tc := range []struct {
		name      string
		model     model.Config
		withdrawn bool
		want      string // "" for a read that goes through
		asked     bool
	}{
		{"local", localModel, false, "", false},
		{"local withdrawn", localModel, true, "the files capability was revoked", false},
		{"cloud withdrawn", cloudModel, true, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, _ := readingGhost(t, "", backend.BudgetConfig{}, "")
			g.modelCfg = tc.model
			rec := &recorder{}
			g.opts.Granted = rec.granted
			g.opts.Refused = rec.refusedCap
			g.opts.Consent = rec.record
			g.opts.Withdrawn = func(string) bool { return tc.withdrawn }
			g.surface = &wlclient.SpriteSurface{}
			if tc.asked {
				answer(t, g, consentAllow)
			}
			res := call(t, g, "list_files", `{}`)
			switch {
			case tc.want == "" && res.IsError:
				t.Errorf("the read was refused: %q", res.Text)
			case tc.want != "" && (!res.IsError || !strings.Contains(res.Text, tc.want)):
				t.Errorf("the read answered %q, want %q", res.Text, tc.want)
			}
			if asked := rec.calls > 0; asked != tc.asked {
				t.Errorf("asked = %v, want %v", asked, tc.asked)
			}
		})
	}
}

// On a cloud model the same read is refused with the policy's words, as
// it always has been.
func TestTrustCloudReads(t *testing.T) {
	g, _ := readingGhost(t, "", backend.BudgetConfig{UnpromptedFilePerHour: 1}, "")
	g.modelCfg = cloudModel
	g.startTurn(false, "idle")
	res := call(t, g, "read_file", `{"path":"notes/todo.md"}`)
	if !res.IsError || !strings.Contains(res.Text, "only when the person asks") {
		t.Errorf("a cloud read on an idle turn answered %q", res.Text)
	}
	if len(g.loadNotes()) != 0 {
		t.Error("a refused read was journalled")
	}
	w := searchingGhost(t, "skills")
	w.modelCfg = cloudModel
	w.startTurn(false, "idle")
	if err := w.allowWeb(); err == nil || !strings.Contains(err.Error(), "where a skill says to") {
		t.Errorf("a cloud search on an idle turn: %v", err)
	}
	// Without a grant, a cloud read on a turn nobody is watching cannot
	// ask, so it is refused.
	g.opts.Granted = func(string) bool { return false }
	g.startTurn(false, "occasion")
	shareFolders(t, g, config.FilesConfig{Unprompted: "any", Roots: g.filesCfg.Roots})
	if res := call(t, g, "list_files", `{}`); !res.IsError || !strings.Contains(res.Text, "not granted") {
		t.Errorf("an ungranted cloud read answered %q", res.Text)
	}
}

// Telling another ghost after a read is put to the person on both; the
// ghost's own bookkeeping is asked about only on cloud, and so is a search
// after a file.
func TestTrustWritesConfined(t *testing.T) {
	for _, cfg := range []model.Config{localModel, cloudModel} {
		g := readGhost(t, true, taintWeb)
		g.modelCfg = cfg
		asked := answerAsking(t, g, consentNotThisTime)
		res := g.call(t.Context(), "tell", json.RawMessage(`{"ghost":"Moth","text":"the password"}`))
		if q := asked(); !strings.Contains(q, "read the web just now and wants to tell Moth") {
			t.Errorf("%s: tell after read_page asked %q", cfg.Name, q)
		}
		if !res.IsError {
			t.Errorf("%s: tell went through a refusal: %q", cfg.Name, res.Text)
		}

		g = readGhost(t, false, taintWeb)
		g.modelCfg = cfg
		if res := g.call(t.Context(), "tell", json.RawMessage(`{"ghost":"Moth","text":"x"}`)); !strings.Contains(res.Text, "on your own") {
			t.Errorf("%s: tell after a read on an event turn answered %q", cfg.Name, res.Text)
		}

		g = readGhost(t, false, taintFile)
		g.modelCfg = cfg
		g.store = &memStore{m: map[string]string{}}
		intend := g.call(t.Context(), "intend", json.RawMessage(`{"text":"buy milk","delay_ms":600000}`))
		search := g.call(t.Context(), "search", json.RawMessage(`{"query":"invoice 4471"}`))
		gated := strings.Contains(intend.Text, "not after reading") && strings.Contains(search.Text, "not after reading")
		free := !strings.Contains(intend.Text, "not after reading") && !strings.Contains(search.Text, "not after reading")
		switch {
		case cfg == cloudModel && !gated:
			t.Errorf("cloud: intend %q and search %q after a file were not gated", intend.Text, search.Text)
		case cfg == localModel && !free:
			t.Errorf("local: intend %q and search %q after a file were gated", intend.Text, search.Text)
		}
	}
}

// Rule five says what the table says: reading on its own only for local,
// and what becomes of a write after a read.
func TestRuleFive(t *testing.T) {
	s := standing{Name: "wisp", Web: true, Unprompted: "skills", Files: true, FilesUnprompted: "never"}
	s.Trust = onLocal
	loc := ruleFive(s)
	s.Trust = onCloud
	cl := ruleFive(s)
	const own = "read their files on your own whenever"
	if !strings.Contains(loc, own) {
		t.Errorf("the local rule does not say it may read files on its own:\n%s", loc)
	}
	if strings.Contains(cl, own) || !strings.Contains(cl, "Read their files only when they ask.") {
		t.Errorf("the cloud rule does not keep to the policy:\n%s", cl)
	}
	if !strings.Contains(loc, "look things up on your own whenever") || !strings.Contains(cl, "only when a skill says to") {
		t.Errorf("the web half differs from the table:\nlocal: %s\ncloud: %s", loc, cl)
	}
	if !strings.Contains(loc, "labelled with where it came from") || strings.Contains(loc, "refused, so keep") {
		t.Errorf("the local rule misstates writes:\n%s", loc)
	}
	if !strings.Contains(cl, "refused, so keep") {
		t.Errorf("the cloud rule does not say an echoing write is refused:\n%s", cl)
	}
	if strings.Contains(loc, "taking something on") || !strings.Contains(cl, "taking something on") {
		t.Error("only the cloud rule should put taking something on to the person after a read")
	}
	for _, r := range []string{loc, cl} {
		if !strings.Contains(r, "speaking to another ghost or using a tool is put to them first") {
			t.Errorf("a rule leaves out that telling is asked about:\n%s", r)
		}
	}
	// A ghost that reads nothing is told none of it.
	s.Web, s.Files = false, false
	if r := ruleFive(s); strings.Contains(r, "put to them first") || strings.Contains(r, "labelled") {
		t.Errorf("a ghost with no reads is told about them:\n%s", r)
	}
}

// memoryGhost is a model ghost with memory, sharing the notes folder,
// trusting where cfg says, on the person's turn, having read todo.md.
func memoryGhost(t *testing.T, cfg model.Config) (*Ghost, *modelBrain) {
	t.Helper()
	g, b, _ := modelGhostWith(t, 0, model.Config{Trust: cfg.Trust})
	if b.mem == nil {
		t.Fatal("the ghost has no memory")
	}
	g.manifest.Ghost.Capabilities = append(g.manifest.Ghost.Capabilities, "files")
	dir, _ := sharedFolder(t)
	shareFolders(t, g, config.FilesConfig{Roots: map[string]string{"notes": dir}})
	tn := g.startTurn(true, "")
	tn.id = 7
	if res := call(t, g, "read_file", `{"path":"notes/todo.md"}`); res.IsError {
		t.Fatalf("reading answered %q", res.Text)
	}
	return g, b
}

// On cloud a write that repeats what the turn read is refused, naming the
// source; the person's own words are theirs to have saved; and a write in
// the ghost's own words is kept.
func TestTrustCloudEchoGate(t *testing.T) {
	g, b := memoryGhost(t, model.Config{Trust: model.TrustCloud})
	for _, c := range []struct{ tool, args string }{
		{"remember", `{"topic":"chores","line":"They need to pay the invoice from the plumber"}`},
		{"rewrite_notes", `{"about":"you","text":"Has to pay the invoice from the plumber soon."}`},
		{"skill_write", `{"name":"chores","body":"---\nname: chores\ndescription: chores\n---\nPay the invoice from the plumber.\n"}`},
	} {
		res := call(t, g, c.tool, c.args)
		if !res.IsError || !strings.Contains(res.Text, "repeats what you read from a file") ||
			!strings.Contains(res.Text, "read_file") || !strings.Contains(res.Text, "invoice from the plumber") {
			t.Errorf("%s echoing a file answered %q", c.tool, res.Text)
		}
	}
	if hits, _ := b.mem.Recall(t.Context(), "plumber", 5); len(hits) != 0 {
		t.Errorf("an echoing line was kept: %+v", hits)
	}
	if res := call(t, g, "remember", `{"topic":"chores","line":"has a plumbing bill to settle"}`); res.IsError {
		t.Errorf("a write in its own words answered %q", res.Text)
	}
	g.current().input = "remind me to pay the invoice from the plumber"
	if res := call(t, g, "remember", `{"topic":"chores","line":"must pay the invoice from the plumber"}`); res.IsError {
		t.Errorf("the person's own words were refused: %q", res.Text)
	}
}

// On local the same writes are kept, each labelled with the file it came
// from and the turn.
func TestTrustLocalLabelsWrites(t *testing.T) {
	g, b := memoryGhost(t, model.Config{Trust: model.TrustLocal})
	if res := call(t, g, "remember", `{"topic":"chores","line":"They need to pay the invoice from the plumber"}`); res.IsError ||
		!strings.Contains(res.Text, "as from file") {
		t.Errorf("remember answered %q", res.Text)
	}
	topic, err := os.ReadFile(filepath.Join(b.memDir, "topics", "chores.md"))
	if err != nil || !strings.Contains(string(topic), "<!-- file t=7 ") {
		t.Errorf("the topic file is %q (%v), want the line labelled file t=7", topic, err)
	}
	// The model's own label is dropped for the driver's.
	if res := call(t, g, "rewrite_notes", `{"about":"you","text":"Owes the plumber.\n<!-- person t=1 2026-01-01 -->"}`); res.IsError {
		t.Fatalf("rewrite_notes answered %q", res.Text)
	}
	_, you, _ := b.mem.Core()
	if !strings.Contains(you, "Owes the plumber.") || !strings.Contains(you, "<!-- file t=7 ") || strings.Contains(you, "person t=1") {
		t.Errorf("the notes are %q, want them labelled file t=7 alone", you)
	}
	res := call(t, g, "skill_write", `{"name":"chores","body":"---\nname: chores\ndescription: Paying bills\n---\nPay the invoice from the plumber.\n"}`)
	if res.IsError || !strings.Contains(res.Text, "as from file") {
		t.Fatalf("skill_write answered %q", res.Text)
	}
	body, err := b.skillHost().View("chores")
	if err != nil || !strings.Contains(body, "<!-- file t=7 ") {
		t.Errorf("the skill is %q (%v), want it labelled file t=7", body, err)
	}
}
