package ghost

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Manifest is ghost.toml, the package descriptor. v1 covers identity, shell
// art, and the brain; capabilities, signing, and AI grants land with the
// packaging phase.
type Manifest struct {
	Ghost GhostSection `toml:"ghost"`
	Shell ShellSection `toml:"shell"`
	Brain BrainSection `toml:"brain"`
}

type GhostSection struct {
	Name    string `toml:"name"`
	Version string `toml:"version"`
	Author  string `toml:"author"`
}

type ShellSection struct {
	// Sprite is the shell image path, relative to the ghost directory.
	// Empty uses the built-in placeholder.
	Sprite string `toml:"sprite"`
}

type BrainSection struct {
	// Wasm is the brain module path, relative to the ghost directory.
	Wasm string `toml:"wasm"`
}

// LoadManifest reads and validates <dir>/ghost.toml.
func LoadManifest(dir string) (*Manifest, error) {
	path := filepath.Join(dir, "ghost.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := toml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if m.Ghost.Name == "" {
		return nil, fmt.Errorf("%s: ghost.name is required", path)
	}
	if m.Brain.Wasm == "" {
		return nil, fmt.Errorf("%s: brain.wasm is required", path)
	}
	for _, rel := range []string{m.Brain.Wasm, m.Shell.Sprite} {
		if rel == "" {
			continue
		}
		if filepath.IsAbs(rel) || !filepath.IsLocal(rel) {
			return nil, fmt.Errorf("%s: path %q must be relative and stay inside the ghost directory", path, rel)
		}
	}
	return &m, nil
}
