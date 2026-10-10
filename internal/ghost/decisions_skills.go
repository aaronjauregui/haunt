package ghost

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aaronjauregui/haunt/internal/skill"
)

// Skills: routines in the agentskills format (internal/skill). The index
// is in the prompt; the model loads one when it fits, and may write its
// own, which are kept under the ghost's directory and curated when they go
// unused. haunt's own and the package's cannot be changed.

func init() {
	register(verb{
		id:     "skill_view",
		desc:   "Load one of the skills listed in your instructions, by name, and follow it.",
		params: []string{"name:string"},
		run:    skillView,
	})
	register(verb{
		id: "skill_write",
		desc: "Save a routine you worked out as a skill of your own, to follow again: name in lowercase " +
			"with hyphens, body a SKILL.md with front matter (name, description) and the steps.",
		params: []string{"name:string", "body:string"},
		run:    skillWrite,
	})
}

// skillsOf is the ghost's skill host, or a refusal when it has none.
func skillsOf(g *Ghost) (skill.Host, error) {
	if g.mind == nil {
		return nil, refuse("you have no skills")
	}
	h := g.mind.skillHost()
	if h == nil {
		return nil, fmt.Errorf("your skills could not be read")
	}
	return h, nil
}

func skillView(_ context.Context, g *Ghost, _ *turn, args json.RawMessage) (string, error) {
	var a struct {
		Name string `json:"name"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	h, err := skillsOf(g)
	if err != nil {
		return "", err
	}
	return h.View(strings.TrimSpace(a.Name))
}

func skillWrite(_ context.Context, g *Ghost, t *turn, args json.RawMessage) (string, error) {
	var a struct {
		Name string `json:"name"`
		Body string `json:"body"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	h, err := skillsOf(g)
	if err != nil {
		return "", err
	}
	// A skill is a write like a remembered line: gated on what the turn
	// read, and labelled with where it came from, as its last line.
	src := provenance(t)
	if err := g.mayRemember(t, a.Body); err != nil {
		return "", err
	}
	if err := h.Write(strings.TrimSpace(a.Name), g.labelled(a.Body, t)); err != nil {
		return "", err
	}
	g.execNote("wrote itself a skill: " + clip(a.Name, 64))
	return fmt.Sprintf("Saved, as from %s. It is in your skills from the next turn.", src), nil
}
