package ghost

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/aaronjauregui/haunt/internal/skill"
	"github.com/aaronjauregui/haunt/skills"
)

// The tools haunt's own skills name and require are the ghost's tools.
// The format checks live in internal/skill; these need the ghost's tool
// table.
func TestHauntSkillTools(t *testing.T) {
	entries, err := fs.ReadDir(skills.Haunt, ".")
	if err != nil || len(entries) == 0 {
		t.Fatalf("no skills embedded (%v)", err)
	}
	backticked := regexp.MustCompile("`([a-z_]+)`")
	for _, e := range entries {
		raw, err := fs.ReadFile(skills.Haunt, e.Name()+"/SKILL.md")
		if err != nil {
			t.Fatal(err)
		}
		f, body, err := skill.Parse(raw)
		if err != nil {
			t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		for _, tool := range f.RequiresTools() {
			if builtinTools[tool].call == nil {
				t.Errorf("%s requires %s, which is not a ghost tool", e.Name(), tool)
			}
		}
		for _, tool := range skill.ToolRefs(body) {
			if builtinTools[tool].call == nil {
				t.Errorf("%s names %s as a tool, which is not a ghost tool", e.Name(), tool)
			}
		}
		// The web and files tools are offered only by a ghost whose
		// package asked for them, so a skill that leans on one has to
		// require it — or another tool of the same capability, which
		// comes and goes with it — and then the index hides the skill
		// where it is missing, or else phrase it as something to do only
		// if it is there. A skill that does neither tells some ghosts to
		// call a tool they have not got.
		for _, m := range backticked.FindAllStringSubmatch(body, -1) {
			tool := m[1]
			declared := builtinTools[tool].declared
			if declared == "" {
				continue
			}
			required := false
			for _, req := range f.RequiresTools() {
				if builtinTools[req].declared == declared {
					required = true
				}
			}
			if required || strings.Contains(body, "If you have `"+tool+"`") {
				continue
			}
			t.Errorf("%s names %s without requiring it or making it conditional", e.Name(), tool)
		}
	}
}
