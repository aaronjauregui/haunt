package ghost

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/aaronjauregui/haunt/internal/skill"
	"github.com/aaronjauregui/haunt/skills"
)

// The tools haunt's own skills name and require are decisions in the
// registry. The format checks live in internal/skill; these need the
// registry.
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
			if decisions[tool] == nil {
				t.Errorf("%s requires %s, which is not a decision", e.Name(), tool)
			}
		}
		for _, tool := range skill.ToolRefs(body) {
			if decisions[tool] == nil {
				t.Errorf("%s names %s as a tool, which is not a decision", e.Name(), tool)
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
			declared := declaredBy(tool)
			if declared == "" {
				continue
			}
			required := false
			for _, req := range f.RequiresTools() {
				if declaredBy(req) == declared {
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

// declaredBy is the capability a decision is offered under, "" for one
// every ghost is offered.
func declaredBy(name string) string {
	if n, ok := decisions[name].(needsDeclared); ok {
		return n.declared()
	}
	return ""
}

// The skills a ghost is shown follow what it is offered: every haunt skill
// for a ghost offered everything, and the web skill hidden from one whose
// package never asked for the web.
func TestSkillIndexFollowsTheRegistry(t *testing.T) {
	all := make([]string, 0, len(decisions))
	for name := range decisions {
		all = append(all, name)
	}
	h, err := skill.Open(skills.Haunt, t.TempDir(), all)
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := fs.ReadDir(skills.Haunt, ".")
	if got := len(h.List()); got != len(entries) {
		t.Errorf("%d of %d haunt skills listed with every decision offered:\n%s", got, len(entries), h.Index())
	}
	var noWeb []string
	for _, name := range all {
		if declaredBy(name) != "web" {
			noWeb = append(noWeb, name)
		}
	}
	h, err = skill.Open(skills.Haunt, t.TempDir(), noWeb)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.Index(), "look-it-up") {
		t.Errorf("look-it-up is listed for a ghost with no web:\n%s", h.Index())
	}
}
