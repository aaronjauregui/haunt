package ghost

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/backend"
	"github.com/aaronjauregui/haunt/internal/config"
	"github.com/aaronjauregui/haunt/internal/control"
	"github.com/aaronjauregui/haunt/internal/files"
)

// sharedFolder is a folder the person shared as notes, with a note that
// mentions an invoice, a picture, a folder inside it, and a symlink that
// leads out of it. It returns the host directory, which the model must
// never see, and the directory the symlink leads to.
func sharedFolder(t *testing.T) (dir, outside string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "host-notes-dir-7f3a")
	outside = filepath.Join(t.TempDir(), "host-outside-dir-91c2")
	for _, d := range []string{dir, filepath.Join(dir, "2026"), outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("todo.md", []byte("# todo\n\n- pay the invoice from the plumber\n- call the dentist\n"))
	write("2026/march.md", []byte("nothing much\n"))
	write("blob.bin", []byte{0, 1, 2, 3, 0, 0xff, 0xfe})
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 16, 16))); err != nil {
		t.Fatal(err)
	}
	write("cat.png", buf.Bytes())
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("not shared\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	return dir, outside
}

// readingGhost is a ghost that may read the person's files: the capability
// declared, dev-mode grants, notes shared, and somewhere to keep a journal,
// so what is under test is the tools and their rules rather than consent.
func readingGhost(t *testing.T, unprompted string, budget backend.BudgetConfig, vision string) (*Ghost, string) {
	t.Helper()
	dir, _ := sharedFolder(t)
	g, _ := choosingGhost(t)
	g.store = &memStore{m: map[string]string{}}
	g.manifest = &Manifest{Ghost: GhostSection{
		Name:         "wisp",
		Capabilities: []string{"llm", "files"},
	}}
	cfg := &backend.Config{Budget: budget}
	if vision != "" {
		cfg.Vision = backend.VisionConfig{BaseURL: vision, Model: "fake"}
	}
	g.backend = backend.NewService(cfg, g.log)
	shareFolders(t, g, config.FilesConfig{
		Unprompted: unprompted,
		Roots:      map[string]string{"notes": dir},
	})
	g.startTurn(true, "")
	return g, dir
}

// shareFolders opens roots on the ghost as applySettings does.
func shareFolders(t *testing.T, g *Ghost, cfg config.FilesConfig) {
	t.Helper()
	g.filesCfg = cfg
	roots, _ := files.Open(cfg.Roots)
	g.files = roots
	t.Cleanup(func() { roots.Close() })
}

// fakeDescriber is a vision model that describes every picture the same
// way, and counts how often it was asked.
func fakeDescriber(t *testing.T) (url string, asked *int) {
	t.Helper()
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		fmt.Fprint(w, `{"choices":[{"message":{"content":"a black square with RECEIPT written on it"}}]}`)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &n
}

func call(t *testing.T, g *Ghost, name, args string) control.ToolResult {
	return (hermesTools{g}).Call(t.Context(), name, json.RawMessage(args))
}

// A ghost whose package never asked for files is not told the file tools
// exist, and a skill that needs one is hidden by hermes rather than the
// ghost offering to read a note and being refused.
func TestFileToolsAreOfferedOnlyWhenDeclared(t *testing.T) {
	g, _ := readingGhost(t, "", backend.BudgetConfig{}, "")
	offered := func() []string {
		var names []string
		for _, s := range (hermesTools{g}).List() {
			names = append(names, s.Name)
		}
		return names
	}
	tools := []string{"list_files", "read_file", "search_files"}
	for _, name := range tools {
		if !slices.Contains(offered(), name) {
			t.Errorf("%s is not offered to a ghost that declared files", name)
		}
	}
	g.manifest.Ghost.Capabilities = []string{"llm"}
	for _, name := range tools {
		if slices.Contains(offered(), name) {
			t.Errorf("%s is offered to a ghost that never asked for files", name)
		}
		res := call(t, g, name, `{"path":"notes/todo.md","text":"invoice"}`)
		if !res.IsError || !strings.Contains(res.Text, "no tool called "+name) {
			t.Errorf("calling %s, not offered, answered %q", name, res.Text)
		}
	}
}

// Each tool, driven the way hermes drives it, against a real folder.
func TestTheFileToolsWorkThroughCall(t *testing.T) {
	url, _ := fakeDescriber(t)
	g, _ := readingGhost(t, "", backend.BudgetConfig{}, url)
	for _, c := range []struct{ tool, args, want string }{
		{"list_files", `{}`, "notes"},
		{"list_files", `{"path":"notes"}`, "todo.md"},
		{"list_files", `{"path":"notes/2026"}`, "march.md"},
		{"read_file", `{"path":"notes/todo.md"}`, "pay the invoice"},
		{"read_file", `{"path":"notes/cat.png","question":"what does it say?"}`, "RECEIPT"},
		{"search_files", `{"text":"INVOICE"}`, "notes/todo.md"},
		{"search_files", `{"text":"march","names_only":true}`, "notes/2026/march.md"},
	} {
		res := call(t, g, c.tool, c.args)
		if res.IsError || !strings.Contains(res.Text, c.want) {
			t.Errorf("%s %s answered %q (error=%v), want it to contain %q", c.tool, c.args, res.Text, res.IsError, c.want)
		}
	}
	// A refusal from the folder rules is put as one.
	res := call(t, g, "read_file", `{"path":"notes/escape/secret.txt"}`)
	if !res.IsError || !strings.HasPrefix(res.Text, "That was not allowed:") {
		t.Errorf("reading through a link out of the folder answered %q", res.Text)
	}
	res = call(t, g, "read_file", `{"path":"notes/missing.md"}`)
	if !res.IsError || !strings.HasPrefix(res.Text, "That did not work:") {
		t.Errorf("reading a missing file answered %q", res.Text)
	}
}

// Nothing a file tool answers, success or failure, names the folder as the
// host knows it: the model sees notes/…, never the directory, and so never
// the person's username either.
func TestNoHostPathReachesTheModel(t *testing.T) {
	url, _ := fakeDescriber(t)
	g, dir := readingGhost(t, "", backend.BudgetConfig{}, url)
	outside := ""
	if target, err := os.Readlink(filepath.Join(dir, "escape")); err == nil {
		outside = target
	}
	// A FIFO, which must be refused rather than opened.
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A folder that is shared but missing, as an unmounted drive is.
	gone := filepath.Join(t.TempDir(), "host-gone-dir-55d0")
	shareFolders(t, g, config.FilesConfig{Roots: map[string]string{"notes": dir, "drive": gone}})
	// And a file that cannot be read.
	locked := filepath.Join(dir, "locked.md")
	os.WriteFile(locked, []byte("private\n"), 0o000)

	calls := []struct{ tool, args string }{
		{"list_files", `{}`},
		{"list_files", `{"path":"notes"}`},
		{"list_files", `{"path":"notes/2026"}`},
		{"list_files", `{"path":"notes/todo.md"}`},
		{"list_files", `{"path":"notes/missing"}`},
		{"list_files", `{"path":"notes/escape"}`},
		{"list_files", `{"path":"notes/../.."}`},
		{"list_files", `{"path":"` + dir + `"}`},
		{"list_files", `{"path":"~/notes"}`},
		{"list_files", `{"path":"notes\\2026"}`},
		{"list_files", `{"path":"nowhere/x"}`},
		{"list_files", `{"path":"drive"}`},
		{"read_file", `{"path":"notes/todo.md"}`},
		{"read_file", `{"path":"notes/todo.md","from":99999}`},
		{"read_file", `{"path":"notes/cat.png"}`},
		{"read_file", `{"path":"notes/blob.bin"}`},
		{"read_file", `{"path":"notes/pipe"}`},
		{"read_file", `{"path":"notes/2026"}`},
		{"read_file", `{"path":"notes/missing.md"}`},
		{"read_file", `{"path":"notes/locked.md"}`},
		{"read_file", `{"path":"notes/escape/secret.txt"}`},
		{"read_file", `{"path":"notes/../../etc/passwd"}`},
		{"read_file", `{"path":"` + filepath.Join(dir, "todo.md") + `"}`},
		{"read_file", `{"path":"~/notes/todo.md"}`},
		{"read_file", `{"path":"notes/a\u0000b"}`},
		{"read_file", `{"path":"drive/x.md"}`},
		{"read_file", `{"path":""}`},
		{"read_file", `{"path":7}`},
		{"search_files", `{"text":"invoice"}`},
		{"search_files", `{"text":"invoice","path":"notes/2026"}`},
		{"search_files", `{"text":"invoice","path":"notes/todo.md"}`},
		{"search_files", `{"text":"invoice","path":"notes/missing"}`},
		{"search_files", `{"text":"invoice","path":"notes/escape"}`},
		{"search_files", `{"text":"invoice","path":"` + dir + `"}`},
		{"search_files", `{"text":"invoice","path":"drive"}`},
		{"search_files", `{"text":""}`},
	}
	for _, c := range calls {
		res := call(t, g, c.tool, c.args)
		for _, host := range []string{dir, outside, gone, filepath.Dir(dir)} {
			if host != "" && strings.Contains(res.Text, host) {
				t.Errorf("%s %s answered with a host path: %q", c.tool, c.args, res.Text)
			}
		}
	}
	// Nor with the vision model failing, whose error goes back as well.
	g.backend = backend.NewService(&backend.Config{
		Vision: backend.VisionConfig{BaseURL: "http://127.0.0.1:1", Model: "fake"},
	}, g.log)
	res := call(t, g, "read_file", `{"path":"notes/cat.png"}`)
	if !res.IsError || strings.Contains(res.Text, dir) {
		t.Errorf("a failed description answered %q", res.Text)
	}
	// Nor with no vision model at all.
	g.backend = backend.NewService(&backend.Config{}, g.log)
	if res := call(t, g, "read_file", `{"path":"notes/cat.png"}`); strings.Contains(res.Text, dir) {
		t.Errorf("a picture with no vision model answered %q", res.Text)
	}
	// Nor in the journal, which rides into every prompt.
	for _, n := range g.loadNotes() {
		if strings.Contains(n.Text, dir) {
			t.Errorf("the journal names a host path: %q", n.Text)
		}
	}
}

// Text from a file marks the turn, so what reaches past the person, or
// would carry the file's words off the machine, is asked about again.
func TestAFileTaintsTheTurn(t *testing.T) {
	url, _ := fakeDescriber(t)
	for _, args := range []string{`{"path":"notes/todo.md"}`, `{"path":"notes/cat.png"}`} {
		g, _ := readingGhost(t, "", backend.BudgetConfig{}, url)
		if res := call(t, g, "read_file", args); res.IsError {
			t.Fatalf("read_file %s: %q", args, res.Text)
		}
		if _, _, tainted := g.thisTurn(); tainted&taintFile == 0 {
			t.Errorf("read_file %s left the turn untainted", args)
		}
	}
	// A read past the end brings back only a note that there is nothing
	// more, and a failed one brings back nothing.
	for _, args := range []string{`{"path":"notes/todo.md","from":99999}`, `{"path":"notes/missing.md"}`} {
		g, _ := readingGhost(t, "", backend.BudgetConfig{}, url)
		call(t, g, "read_file", args)
		if _, _, tainted := g.thisTurn(); tainted != 0 {
			t.Errorf("read_file %s tainted the turn", args)
		}
	}
}

// A list of names is the person's own organising, not somebody else's
// words.
func TestListingDoesNotTaint(t *testing.T) {
	g, _ := readingGhost(t, "", backend.BudgetConfig{}, "")
	for _, args := range []string{`{}`, `{"path":"notes"}`, `{"path":"notes/todo.md"}`} {
		if res := call(t, g, "list_files", args); res.IsError {
			t.Fatalf("list_files %s: %q", args, res.Text)
		}
	}
	if _, _, tainted := g.thisTurn(); tainted != 0 {
		t.Error("listing tainted the turn")
	}
}

// A search taints only when a line from inside a file came back; names
// alone are not a file's words.
func TestSearchTaintsOnlyWithContentHits(t *testing.T) {
	for _, c := range []struct {
		args    string
		tainted bool
	}{
		{`{"text":"invoice"}`, true},
		{`{"text":"todo","names_only":true}`, false},
		{`{"text":"march"}`, false},
		{`{"text":"no such words anywhere"}`, false},
	} {
		g, _ := readingGhost(t, "", backend.BudgetConfig{}, "")
		if res := call(t, g, "search_files", c.args); res.IsError {
			t.Fatalf("search_files %s: %q", c.args, res.Text)
		}
		if _, _, tainted := g.thisTurn(); (tainted&taintFile != 0) != c.tainted {
			t.Errorf("search_files %s: tainted=%v, want %v", c.args, tainted != 0, c.tainted)
		}
	}
}

// Files start closed: on a turn nobody started, the [files] unprompted
// policy decides, then quiet hours, then a ration of its own that only
// such a turn spends.
func TestUnpromptedFilesFollowThePolicy(t *testing.T) {
	cases := []struct {
		mode    string
		person  bool
		kind    string
		allowed bool
	}{
		{"", true, "", true},
		{"", false, "occasion", false},
		{"never", false, "occasion", false},
		{"never", true, "", true},
		{"skills", false, "occasion", true},
		{"skills", false, "boot", true},
		{"skills", false, "idle", false},
		{"skills", false, "observed", false},
		{"any", false, "idle", true},
		{"any", false, "observed", true},
	}
	for _, c := range cases {
		g, _ := readingGhost(t, c.mode, backend.BudgetConfig{}, "")
		g.startTurn(c.person, c.kind)
		res := call(t, g, "read_file", `{"path":"notes/todo.md"}`)
		if res.IsError == c.allowed {
			t.Errorf("unprompted=%q person=%v kind=%q: answered %q, want allowed=%v",
				c.mode, c.person, c.kind, res.Text, c.allowed)
		}
		if !c.allowed && !strings.Contains(res.Text, "only when the person asks") {
			t.Errorf("unprompted=%q kind=%q: the refusal %q does not say why", c.mode, c.kind, res.Text)
		}
	}

	// Quiet hours refuse, and cost nothing.
	g, _ := readingGhost(t, "any", backend.BudgetConfig{UnpromptedFilePerHour: 1}, "")
	g.quiet.Away(time.Now().Add(time.Hour))
	g.startTurn(false, "idle")
	for range 3 {
		if res := call(t, g, "list_files", `{}`); !res.IsError || !strings.Contains(res.Text, "quiet time") {
			t.Errorf("a read in quiet hours answered %q", res.Text)
		}
	}
	g.quiet.Home()

	// The ration is spent only by a turn nobody started: the person asking
	// costs nothing, a refused policy costs nothing, and the one read the
	// hour allows is the only one.
	g.startTurn(true, "")
	for range 3 {
		if res := call(t, g, "read_file", `{"path":"notes/todo.md"}`); res.IsError {
			t.Fatalf("the person's own read was refused: %q", res.Text)
		}
	}
	g.startTurn(false, "idle")
	if res := call(t, g, "read_file", `{"path":"notes/todo.md"}`); res.IsError {
		t.Fatalf("the one unprompted read the hour allows was refused: %q", res.Text)
	}
	if res := call(t, g, "read_file", `{"path":"notes/todo.md"}`); !res.IsError {
		t.Error("a second unprompted read went through a ration of one")
	}
}

// With nothing shared there is nothing to read, whatever the package
// asked for.
func TestNoFoldersSharedSaysSo(t *testing.T) {
	g, _ := readingGhost(t, "any", backend.BudgetConfig{}, "")
	shareFolders(t, g, config.FilesConfig{})
	for _, tool := range []string{"list_files", "read_file", "search_files"} {
		res := call(t, g, tool, `{"path":"notes/todo.md","text":"invoice"}`)
		if !res.IsError || !strings.Contains(res.Text, "no folders are shared") {
			t.Errorf("%s with nothing shared answered %q", tool, res.Text)
		}
	}
}

// A picture is described by the vision model, and that is charged to the
// vision ration like any other look.
func TestAPictureSpendsTheVisionBudget(t *testing.T) {
	url, asked := fakeDescriber(t)
	g, _ := readingGhost(t, "", backend.BudgetConfig{VisionRequestsPerHour: 1}, url)
	res := call(t, g, "read_file", `{"path":"notes/cat.png"}`)
	if res.IsError || !strings.Contains(res.Text, "RECEIPT") || !strings.Contains(res.Text, "notes/cat.png") {
		t.Fatalf("reading a picture answered %q", res.Text)
	}
	res = call(t, g, "read_file", `{"path":"notes/cat.png"}`)
	if !res.IsError || !strings.Contains(res.Text, "vision budget") {
		t.Errorf("a second picture on a vision ration of one answered %q", res.Text)
	}
	if *asked != 1 {
		t.Errorf("the vision model was asked %d times, want 1", *asked)
	}
}

// Without a vision model a picture cannot be read, and the model is told
// so rather than handed nothing.
func TestNoVisionModelSaysSo(t *testing.T) {
	g, _ := readingGhost(t, "", backend.BudgetConfig{}, "")
	res := call(t, g, "read_file", `{"path":"notes/cat.png"}`)
	if !res.IsError || !strings.Contains(res.Text, "no vision model") {
		t.Errorf("a picture with no vision model answered %q", res.Text)
	}
	if _, _, tainted := g.thisTurn(); tainted != 0 {
		t.Error("a picture nobody described tainted the turn")
	}
}

// What a ghost read goes in its journal, where the person can see it, by
// the name the person gave the folder.
func TestReadFileJournals(t *testing.T) {
	url, _ := fakeDescriber(t)
	g, _ := readingGhost(t, "", backend.BudgetConfig{}, url)
	for _, c := range []struct{ tool, args string }{
		{"read_file", `{"path":"notes/todo.md"}`},
		{"read_file", `{"path":"notes/cat.png"}`},
		{"list_files", `{"path":"notes/2026"}`},
		{"list_files", `{}`},
		{"search_files", `{"text":"invoice"}`},
		{"read_file", `{"path":"notes/missing.md"}`},
	} {
		call(t, g, c.tool, c.args)
	}
	var got []string
	for _, n := range g.loadNotes() {
		got = append(got, n.Text)
	}
	want := []string{
		"read notes/todo.md",
		"looked at notes/cat.png",
		"listed notes/2026",
		"listed the shared folders",
		`searched files for "invoice" (1 match)`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("journal = %q, want %q", got, want)
	}
}

// SOUL.md is told the ghost can read files only when there is something
// to read, and is told neither the folders' names nor where they are.
func TestThePersonaNamesNoFolders(t *testing.T) {
	g, dir := readingGhost(t, "skills", backend.BudgetConfig{}, "")
	p := g.hermesPersona()
	if !p.Files || p.FilesUnprompted != "skills" {
		t.Errorf("persona files = %v %q", p.Files, p.FilesUnprompted)
	}
	if !slices.Contains(p.Abilities, "read the files in folders they have shared with you") {
		t.Errorf("abilities %q do not include reading files", p.Abilities)
	}
	if s := fmt.Sprint(p); strings.Contains(s, dir) || strings.Contains(s, "notes") {
		t.Error("the persona names a shared folder or its host path")
	}

	shareFolders(t, g, config.FilesConfig{})
	if p := g.hermesPersona(); p.Files || len(p.Abilities) != 0 {
		t.Errorf("with nothing shared the persona says files=%v, abilities %q", p.Files, p.Abilities)
	}
}
