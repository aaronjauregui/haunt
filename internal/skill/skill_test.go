package skill

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/aaronjauregui/haunt/skills"
)

// allTools is every tool haunt's own skills require.
var allTools = []string{
	"ask", "ask_text", "draw", "show", "pictures", "style", "search", "read_page",
	"list_files", "read_file", "search_files", "intend", "resolve", "set_aside", "intentions",
}

func without(list []string, drop ...string) []string {
	return slices.DeleteFunc(slices.Clone(list), func(s string) bool { return slices.Contains(drop, s) })
}

func open(t *testing.T, embedded fs.FS, tools []string) (*host, string) {
	t.Helper()
	dir := t.TempDir()
	h, err := Open(embedded, dir, tools)
	if err != nil {
		t.Fatal(err)
	}
	return h.(*host), dir
}

func names(list []Skill) []string {
	var out []string
	for _, s := range list {
		out = append(out, s.Name)
	}
	return out
}

// Haunt's own skills parse, are named for their directories, and say
// when they apply in what the index shows of them.
func TestHauntSkills(t *testing.T) {
	entries, err := fs.ReadDir(skills.Haunt, ".")
	if err != nil || len(entries) == 0 {
		t.Fatalf("no skills embedded (%v)", err)
	}
	for _, e := range entries {
		raw, err := fs.ReadFile(skills.Haunt, e.Name()+"/SKILL.md")
		if err != nil {
			t.Fatal(err)
		}
		f, body, err := Parse(raw)
		if err != nil {
			t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		if f.Name != e.Name() || !ValidName(f.Name) || len(f.Description) > IndexDescription {
			t.Errorf("%s: name %q, description %q", e.Name(), f.Name, f.Description)
		}
		if strings.Contains(body, "mcp__") {
			t.Errorf("%s names a tool with a server prefix", e.Name())
		}
		for _, tool := range f.RequiresTools() {
			if !slices.Contains(allTools, tool) {
				t.Errorf("%s requires %s, which is not in allTools", e.Name(), tool)
			}
		}
	}
}

func TestIndex(t *testing.T) {
	h, _ := open(t, skills.Haunt, allTools)
	want := []string{"draw-on-request", "focus-session", "keep-an-eye-on-a-goal", "look-it-up",
		"morning-check-in", "read-my-files", "weekly-review"}
	if got := names(h.List()); !slices.Equal(got, want) {
		t.Fatalf("List() = %v, want %v", got, want)
	}
	idx := h.Index()
	if n := strings.Count(idx, "\n"); n != 7 {
		t.Errorf("index has %d lines:\n%s", n, idx)
	}
	if !strings.Contains(idx, "- look-it-up: Asked to look up, search, or check something online\n") {
		t.Errorf("index:\n%s", idx)
	}

	h, _ = open(t, skills.Haunt, without(allTools, "search"))
	if got := names(h.List()); slices.Contains(got, "look-it-up") || len(got) != 6 {
		t.Errorf("without search, List() = %v", got)
	}
	if strings.Contains(h.Index(), "look-it-up") {
		t.Errorf("without search, the index shows look-it-up:\n%s", h.Index())
	}
	if _, err := h.View("look-it-up"); err == nil {
		t.Error("a hidden skill can be viewed")
	}
}

func TestIndexCutsAndMarks(t *testing.T) {
	long := strings.Repeat("abcdefghij", 8)
	h, _ := open(t, fstest.MapFS{}, []string{"ask"})
	if err := h.Write("tidy-desk", "---\nname: tidy-desk\ndescription: "+long+"\n---\nAsk them first with `ask`.\n"); err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSuffix(h.Index(), "\n")
	_, desc, _ := strings.Cut(line, ": ")
	if !strings.HasPrefix(line, "- tidy-desk (learned): ") || len([]rune(desc)) != IndexDescription || !strings.HasSuffix(desc, "…") {
		t.Errorf("index line %q", line)
	}
}

func TestView(t *testing.T) {
	h, _ := open(t, skills.Haunt, allTools)
	body, err := h.View("focus-session")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(body, "# Focus session") || strings.Contains(body, "requires_tools") || strings.Contains(body, "Files in this skill") {
		t.Errorf("View = %.80q", body)
	}

	h, dir := open(t, nil, []string{"ask"})
	if err := h.Write("tidy-desk", "---\nname: tidy-desk\ndescription: Tidy.\n---\n# Tidy\n"); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(dir, "tidy-desk", "notes"), 0o700)
	os.WriteFile(filepath.Join(dir, "tidy-desk", "notes", "drawers.md"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(dir, "tidy-desk", "checklist.md"), []byte("x"), 0o600)
	body, err = h.View("tidy-desk")
	if err != nil {
		t.Fatal(err)
	}
	if body != "# Tidy\n\nFiles in this skill: checklist.md, notes/drawers.md\n" {
		t.Errorf("View = %q", body)
	}
	if _, err := h.View("nothing"); err == nil {
		t.Error("View of an unknown skill succeeded")
	}
}

func TestWrite(t *testing.T) {
	h, dir := open(t, skills.Haunt, allTools)
	body := "---\nname: tea-break\ndescription: When they have been at it for hours\n" +
		"metadata:\n  haunt:\n    requires_tools: [intend]\n---\n# Tea\n\nCall `intend` with `delay_ms` 600000.\n"
	if err := h.Write("tea-break", body); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "tea-break", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	f, text, err := Parse(data)
	if err != nil || f.Name != "tea-break" || !slices.Equal(f.RequiresTools(), []string{"intend"}) || !strings.HasPrefix(text, "# Tea") {
		t.Errorf("written %q (%v)", data, err)
	}
	// A fresh host finds it.
	h2, err := Open(skills.Haunt, dir, allTools)
	if err != nil {
		t.Fatal(err)
	}
	if got := h2.List(); got[len(got)-1].Name != "tea-break" || !got[len(got)-1].Learned {
		t.Errorf("List() = %v", got)
	}
	// Replacing it works; haunt's own, bad names, and bad bodies do not.
	if err := h.Write("tea-break", strings.Replace(body, "# Tea", "# Tea, again", 1)); err != nil {
		t.Errorf("replace: %v", err)
	}
	for _, c := range []struct{ name, body string }{
		{"focus-session", "---\nname: focus-session\ndescription: x\n---\n"},
		{"Bad Name", "---\nname: Bad Name\ndescription: x\n---\n"},
		{ArchiveDir, "---\nname: archive\ndescription: x\n---\n"},
		{"no-front", "# just a body\n"},
		{"other-name", "---\nname: tea-break\ndescription: x\n---\n"},
		{"too-big", "---\nname: too-big\ndescription: x\n---\n" + strings.Repeat("x", MaxBody)},
	} {
		if err := h.Write(c.name, c.body); err == nil {
			t.Errorf("Write(%q) succeeded", c.name)
		} else if strings.Contains(err.Error(), dir) {
			t.Errorf("Write(%q) error names the directory: %v", c.name, err)
		}
	}
}

func TestWriteRefusesUnknownTool(t *testing.T) {
	h, dir := open(t, skills.Haunt, allTools)
	body := "---\nname: clean-up\ndescription: Clear their downloads\n---\n" +
		"Call `run_shell` with `rm -rf ~/Downloads/*`, then `ask` if it helped.\n"
	err := h.Write("clean-up", body)
	if err == nil || !strings.Contains(err.Error(), "run_shell") {
		t.Fatalf("Write = %v, want a refusal naming run_shell", err)
	}
	if _, serr := os.Stat(filepath.Join(dir, "clean-up")); serr == nil {
		t.Error("a refused skill was written")
	}
	// A required tool the ghost has not got is refused the same way.
	err = h.Write("clean-up", "---\nname: clean-up\ndescription: x\nmetadata:\n  haunt:\n    requires_tools: [look]\n---\nx\n")
	if err == nil || !strings.Contains(err.Error(), "look") {
		t.Errorf("Write = %v, want a refusal naming look", err)
	}
}

func TestToolRefs(t *testing.T) {
	text := "Call `intend` with `delay_ms` set. Then `notes__append(text)`, or use the `look` tool; " +
		"`terminal` is not yours, and `like` is a parameter. Calling the `ask` again is fine."
	want := []string{"intend", "notes__append", "look", "terminal", "ask"}
	if got := ToolRefs(text); !slices.Equal(got, want) {
		t.Errorf("ToolRefs = %v, want %v", got, want)
	}
	// What haunt's own skills name is what they require, or a verb every
	// ghost has.
	entries, _ := fs.ReadDir(skills.Haunt, ".")
	for _, e := range entries {
		raw, _ := fs.ReadFile(skills.Haunt, e.Name()+"/SKILL.md")
		_, body, _ := Parse(raw)
		for _, tool := range ToolRefs(body) {
			if !slices.Contains(allTools, tool) && !slices.Contains([]string{"face", "look", "say", "speak", "stay_silent"}, tool) {
				t.Errorf("%s names %s", e.Name(), tool)
			}
		}
	}
}

func TestCurate(t *testing.T) {
	h, dir := open(t, skills.Haunt, allTools)
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	write := func(name string, viewed time.Time) {
		h.now = func() time.Time { return viewed }
		if err := h.Write(name, "---\nname: "+name+"\ndescription: x\n---\nx\n"); err != nil {
			t.Fatal(err)
		}
	}
	write("old-one", now.Add(-31*24*time.Hour))
	write("dusty-one", now.Add(-15*24*time.Hour))
	write("fresh-one", now.Add(-2*24*time.Hour))
	// The archive keeps the latest of a name.
	os.MkdirAll(filepath.Join(dir, ArchiveDir, "old-one"), 0o700)

	h.now = func() time.Time { return now }
	if err := h.Curate(now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ArchiveDir, "old-one", "SKILL.md")); err != nil {
		t.Errorf("old-one is not in the archive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "old-one")); err == nil {
		t.Error("old-one is still live")
	}
	stale := map[string]bool{}
	for _, s := range h.List() {
		if s.Learned {
			stale[s.Name] = s.Stale
		}
	}
	if len(stale) != 2 || !stale["dusty-one"] || stale["fresh-one"] {
		t.Errorf("learned after Curate: %v", stale)
	}
	if !strings.Contains(h.Index(), "- dusty-one (learned, stale): ") {
		t.Errorf("index:\n%s", h.Index())
	}

	// Viewing it makes it fresh, and a later host remembers.
	if _, err := h.View("dusty-one"); err != nil {
		t.Fatal(err)
	}
	h2, err := Open(skills.Haunt, dir, allTools)
	if err != nil {
		t.Fatal(err)
	}
	if err := h2.Curate(now.Add(13 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, s := range h2.List() {
		if s.Learned {
			got[s.Name] = s.Stale
		}
	}
	if len(got) != 2 || got["dusty-one"] || !got["fresh-one"] {
		t.Errorf("learned after a second Curate: %v", got)
	}

	// Haunt's own are never curated.
	if err := h2.Curate(now.Add(365 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := len(h2.List()); n != 7 {
		t.Errorf("after a year, %d skills", n)
	}
}

func TestLearnedSkipsBroken(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "broken"), 0o700)
	os.WriteFile(filepath.Join(dir, "broken", "SKILL.md"), []byte("no front matter"), 0o600)
	os.MkdirAll(filepath.Join(dir, "focus-session"), 0o700)
	os.WriteFile(filepath.Join(dir, "focus-session", "SKILL.md"), []byte("---\nname: focus-session\ndescription: mine\n---\n"), 0o600)
	h, err := Open(skills.Haunt, dir, allTools)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range h.List() {
		if s.Learned {
			t.Errorf("loaded %v", s)
		}
	}
}
