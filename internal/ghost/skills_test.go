package ghost

import (
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/aaronjauregui/haunt/skills"
	"gopkg.in/yaml.v3"
)

// The skills haunt ships parse as hermes reads them, and are hidden
// anywhere the haunt MCP server is not.
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
		head, _, ok := strings.Cut(strings.TrimPrefix(string(raw), "---\n"), "\n---\n")
		if !ok || !strings.HasPrefix(string(raw), "---\n") {
			t.Errorf("%s: no front matter", e.Name())
			continue
		}
		var fm struct {
			Name        string
			Description string
			Metadata    struct {
				Hermes struct {
					RequiresToolsets []string `yaml:"requires_toolsets"`
					RequiresTools    []string `yaml:"requires_tools"`
				}
			}
		}
		if err := yaml.Unmarshal([]byte(head), &fm); err != nil {
			t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		// Hermes's index shows a skill by its name and finds it by its
		// directory, so they must agree, and cuts the description at 60
		// characters (SKILL_PROMPT_DESC_LIMIT in agent/skill_utils.py),
		// which is all the model sees before it chooses to load one.
		if fm.Name != e.Name() || len(fm.Name) > 64 || fm.Description == "" || len(fm.Description) > 60 {
			t.Errorf("%s: name %q, description %q", e.Name(), fm.Name, fm.Description)
		}
		if !slices.Equal(fm.Metadata.Hermes.RequiresToolsets, []string{"mcp-haunt"}) {
			t.Errorf("%s: requires_toolsets = %v, want [mcp-haunt]", e.Name(), fm.Metadata.Hermes.RequiresToolsets)
		}
		// Every tool it names is one the ghost has.
		named := map[string]bool{}
		for _, m := range regexp.MustCompile(`mcp__haunt__([a-z_]+)`).FindAllStringSubmatch(string(raw), -1) {
			named[m[1]] = true
			if builtinTools[m[1]].call == nil {
				t.Errorf("%s names mcp__haunt__%s, which is not a ghost tool", e.Name(), m[1])
			}
		}
		// The web tools are offered only by a ghost whose package asked
		// for them, so a skill that leans on one has to say it needs it —
		// and then hermes hides the skill where it is missing — or else
		// phrase it as something to do only if it is there. A skill that
		// does neither tells some ghosts to call a tool they have not got.
		for _, tool := range []string{"search", "read_page"} {
			if !named[tool] {
				continue
			}
			if slices.Contains(fm.Metadata.Hermes.RequiresTools, "mcp__haunt__"+tool) ||
				strings.Contains(string(raw), "If you have `mcp__haunt__"+tool+"`") {
				continue
			}
			t.Errorf("%s names mcp__haunt__%s without requiring it or making it conditional", e.Name(), tool)
		}
	}
}
