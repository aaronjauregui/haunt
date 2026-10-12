package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aaronjauregui/haunt/internal/ghost"
)

// TestExampleGhostLoads: every example under examples/ with a manifest
// loads as haunt would load it, with nothing to warn about. A persona
// example (ember) is what docs/ghosts/local-models.md quotes, so it must
// also have a character to quote: a system text whose first sentence (the
// reminder that closes every prompt) names the ghost, and examples with
// both sides written. Whisper has no manifest of its own (the demo writes
// one around its brain), so it is not here.
func TestExampleGhostLoads(t *testing.T) {
	dirs, err := filepath.Glob("../../examples/*/ghost.toml")
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) == 0 {
		t.Fatal("no example manifests under examples/")
	}
	seen := map[string]bool{}
	for _, path := range dirs {
		dir := filepath.Dir(path)
		t.Run(filepath.Base(dir), func(t *testing.T) {
			m, err := ghost.LoadManifest(dir)
			if err != nil {
				t.Fatal(err)
			}
			seen[m.Ghost.Name] = true
			if m.Ghost.Name != filepath.Base(dir) {
				t.Errorf("ghost.name %q, want the directory's name %q", m.Ghost.Name, filepath.Base(dir))
			}
			if w := m.Warnings(); len(w) > 0 {
				t.Errorf("warnings: %v", w)
			}
			if !m.Brain.Builtin() {
				return
			}
			system := strings.TrimSpace(m.Persona.System)
			first, _, _ := strings.Cut(system, "\n")
			if !strings.Contains(strings.ToLower(first), strings.ToLower(m.Ghost.Name)) {
				t.Errorf("the first line of persona.system does not name the ghost: %q", first)
			}
			if len(m.Persona.Examples) == 0 {
				t.Error("a persona example with no [[persona.example]]")
			}
			for i, e := range m.Persona.Examples {
				if strings.TrimSpace(e.User) == "" || strings.TrimSpace(e.Ghost) == "" {
					t.Errorf("persona.example %d has an empty side", i)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "README.md")); err != nil {
				t.Errorf("no README.md: %v", err)
			}
		})
	}
	if !seen["ember"] {
		t.Error("examples/ember did not load")
	}
}
