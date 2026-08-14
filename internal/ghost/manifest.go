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

// KnownCapabilities are the capabilities a manifest may request. Unknown
// names are rejected at load so typos fail early and old drivers refuse
// packages built for newer ones.
var KnownCapabilities = map[string]string{
	"message": "receive text messages from outside (haunt say, scripts)",
}

type GhostSection struct {
	Name    string `toml:"name"`
	Version string `toml:"version"`
	Author  string `toml:"author"`
	// Capabilities the ghost requests; each needs a user grant to activate.
	Capabilities []string `toml:"capabilities"`
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
	for _, c := range m.Ghost.Capabilities {
		if _, ok := KnownCapabilities[c]; !ok {
			return nil, fmt.Errorf("%s: unknown capability %q", path, c)
		}
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
