package hermes

import (
	"slices"
	"testing"
	"testing/fstest"
)

// Hermes names a re-exported tool mcp__haunt__<server>__<tool>, dashes
// folded; a skill that needs one is visible only if it spells it so.
func TestUnmet(t *testing.T) {
	skill := func(meta string) *fstest.MapFile {
		return &fstest.MapFile{Data: []byte("---\nname: x\ndescription: x\nmetadata:\n  hermes:\n" + meta + "---\nbody\n")}
	}
	fsys := fstest.MapFS{
		"calendar/SKILL.md":  skill("    requires_toolsets: [mcp-haunt]\n    requires_tools: [mcp__haunt__my_cal__list_events]\n"),
		"dashed/SKILL.md":    skill("    requires_tools: [mcp__haunt__my-cal__list-events]\n"),
		"wrong-set/SKILL.md": skill("    requires_toolsets: [haunt, memory]\n"),
		"shell/SKILL.md":     skill("    requires_tools: [terminal, memory]\n"),
		"plain/SKILL.md":     {Data: []byte("---\nname: plain\ndescription: x\n---\n")},
	}
	got, err := Unmet(fsys, []string{"say", "my-cal__list-events"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"dashed":    {"tool mcp__haunt__my-cal__list-events"},
		"wrong-set": {"toolset haunt"},
		"shell":     {"tool terminal"},
	}
	if len(got) != len(want) {
		t.Fatalf("Unmet = %v, want %v", got, want)
	}
	for k, v := range want {
		if !slices.Equal(got[k], v) {
			t.Errorf("Unmet[%s] = %v, want %v", k, got[k], v)
		}
	}
	if n := MCPToolName("my-cal__list.events"); n != "mcp__haunt__my_cal__list_events" {
		t.Errorf("MCPToolName = %s", n)
	}
}
