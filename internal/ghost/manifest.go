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
	Ghost    GhostSection     `toml:"ghost"`
	Shell    ShellSection     `toml:"shell"`
	Brain    BrainSection     `toml:"brain"`
	Persona  PersonaSection   `toml:"persona"`
	Triggers []TriggerSection `toml:"trigger"`
}

// PersonaSection shapes LLM-generated dialogue. Only meaningful with the
// llm capability.
type PersonaSection struct {
	// System is the character's system prompt.
	System string `toml:"system"`
	// HistoryTurns caps how many past exchanges accompany each request
	// (default 12).
	HistoryTurns int `toml:"history_turns"`
}

// TriggerSection is a scheduled event: at each cron time, an EventCron with
// the given name reaches the brain.
type TriggerSection struct {
	// Cron is a standard 5-field cron expression (minute granularity).
	Cron string `toml:"cron"`
	// Name identifies the trigger in the delivered event.
	Name string `toml:"name"`
}

// KnownCapabilities are the capabilities a manifest may request. Unknown
// names are rejected at load so typos fail early and old drivers refuse
// packages built for newer ones.
var KnownCapabilities = map[string]string{
	"message":      "receive text messages from outside (haunt say, scripts)",
	"tell":         "send messages to other running ghosts",
	"native-brain": "run its brain as a native process instead of the WASM sandbox",
	"llm":          "generate dialogue with your configured language model",
	"imagine":      "generate images with your configured image model",
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
	// Command runs the brain as a native process (argv; argv[0] relative to
	// the ghost directory) speaking the same stdio JSONL protocol. Requires
	// the native-brain capability. Mutually exclusive with Wasm.
	Command []string `toml:"command"`
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
	hasWasm := m.Brain.Wasm != ""
	hasCmd := len(m.Brain.Command) > 0
	if hasWasm == hasCmd {
		return nil, fmt.Errorf("%s: exactly one of brain.wasm or brain.command is required", path)
	}
	if hasCmd && !hasCapability(&m, "native-brain") {
		return nil, fmt.Errorf("%s: brain.command requires the native-brain capability", path)
	}
	for i, tr := range m.Triggers {
		if tr.Cron == "" || tr.Name == "" {
			return nil, fmt.Errorf("%s: trigger %d needs both cron and name", path, i)
		}
	}
	for _, c := range m.Ghost.Capabilities {
		if _, ok := KnownCapabilities[c]; !ok {
			return nil, fmt.Errorf("%s: unknown capability %q", path, c)
		}
	}
	rels := []string{m.Brain.Wasm, m.Shell.Sprite}
	if hasCmd {
		rels = append(rels, m.Brain.Command[0])
	}
	for _, rel := range rels {
		if rel == "" {
			continue
		}
		if filepath.IsAbs(rel) || !filepath.IsLocal(rel) {
			return nil, fmt.Errorf("%s: path %q must be relative and stay inside the ghost directory", path, rel)
		}
	}
	return &m, nil
}

func hasCapability(m *Manifest, cap string) bool {
	for _, c := range m.Ghost.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}
