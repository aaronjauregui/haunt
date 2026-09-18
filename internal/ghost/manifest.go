package ghost

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/aaronjauregui/haunt/internal/proto"
	"github.com/aaronjauregui/haunt/internal/shell"
	hermesskills "github.com/aaronjauregui/haunt/third_party/hermes-skills"
)

// Manifest is ghost.toml, the package descriptor: identity and
// capabilities, the shell, a persona or a brain, tool servers and triggers.
// docs/ghosts/manifest.md is the field reference.
type Manifest struct {
	Ghost    GhostSection     `toml:"ghost"`
	Shell    ShellSection     `toml:"shell"`
	Brain    BrainSection     `toml:"brain"`
	Persona  PersonaSection   `toml:"persona"`
	Hermes   HermesSection    `toml:"hermes"`
	Triggers []TriggerSection `toml:"trigger"`
	Tools    []ToolSection    `toml:"tool"`
}

// PersonaSection is the character a ghost on hermes plays. Only
// meaningful with the llm capability.
type PersonaSection struct {
	// System is the character's text, which leads the hermes profile's
	// SOUL.md (docs/ghosts/persona-and-skills.md).
	System string `toml:"system"`
}

// HermesSection is what a ghost on hermes asks of hermes itself.
type HermesSection struct {
	// BundledSkills names skills from hermes's own library to install in
	// the ghost's profile. Profiles opt out of that library, since nearly
	// all of it needs a shell, files or the web; only the few in
	// third_party/hermes-skills may be named.
	BundledSkills []string `toml:"bundled_skills"`
}

// ToolSection declares one MCP tool server the ghost would like to act
// through. The driver runs it, so the ghost package never contains a
// credential and the brain never holds one — the same division as the AI
// backends, and for the same reason.
//
// Declaring a server is not permission to use it. The `tools` capability
// says the ghost may act at all, and each individual tool is consented to
// the first time it is actually about to be used: a server that offers
// "read a note" and "delete everything" is one grant in the manifest and
// two very different questions to put to a person.
type ToolSection struct {
	// Name identifies the server, and is the first half of every tool's
	// name ("notes" makes "notes.search"). It is the name the user sees in
	// the consent question and in `haunt perms`, so it should say what the
	// server is for rather than what it is called on disk.
	Name string `toml:"name"`
	// Command is the server process (argv). Unlike brain.command, argv[0]
	// is resolved on PATH as well as relative to the ghost directory: a
	// tool server is ordinarily something the *user* installed, not
	// something the ghost shipped, and requiring it inside the package
	// would mean every ghost vendoring its own copy.
	Command []string `toml:"command"`
	// Env names environment variables to pass through from the driver's
	// own environment. Named, never valued: a token belongs in the user's
	// environment, and a manifest that could carry one would be a package
	// that leaks whoever built it.
	Env []string `toml:"env"`
	// Allow, when set, restricts which of the server's tools this ghost may
	// use at all — the manifest's own upper bound, under the user's. Empty
	// means every tool the server offers, which is the ordinary case for a
	// server chosen for this ghost.
	Allow []string `toml:"allow"`
}

// TriggerSection is a scheduled event: at each cron time, an EventCron with
// the given name reaches the brain.
//
// With Tool set it is a poll rather than an occasion: the driver calls the
// tool on schedule and delivers what it said as the event's Text. That one
// field is the whole of haunt's integration story — Home Assistant is an
// MCP server and a trigger line, Nextcloud is an MCP server and a trigger
// line, and whatever you use next year is an MCP server and a trigger line.
// See poll.go for what the driver does with it and why a poll never speaks.
type TriggerSection struct {
	// Cron is a standard 5-field cron expression (minute granularity).
	Cron string `toml:"cron"`
	// Name identifies the trigger in the delivered event.
	Name string `toml:"name"`
	// Tool is what to call on schedule, in the "server.tool" form
	// everything else spells tools — the server as this manifest named it,
	// and the tool as the server calls itself. Empty makes this an
	// occasion, which is the original meaning of a trigger: a name and a
	// time, with the brain finding the words.
	//
	// Declaring one is not permission to run it. The `tools` capability is
	// the standing question, the individual tool is consented to like any
	// other, and the question a poll asks says out loud that it is asking
	// about something that will happen on a schedule rather than about
	// something happening now.
	Tool string `toml:"tool"`
	// Args is the argument object handed to the tool, unchanged, on every
	// poll. Written inline in the manifest — `args = { within = "2h" }` —
	// because a poll asks the same question every time; anything that has
	// to vary is a thing the model should be asking for during a
	// conversation, not a schedule.
	Args map[string]any `toml:"args"`
	// QuietWhen makes this a presence poll: when the tool's answer contains
	// this text, the driver treats the house as empty and keeps quiet until
	// an answer that does not.
	//
	// The vocabulary is here rather than in the driver on purpose. Every
	// presence server words it differently — "not_home", "away", "false" —
	// and a driver that knew any of those words would have an integration
	// in it, which is the one thing this direction is built to avoid. So
	// the manifest carries the vendor's word and the driver carries none.
	//
	// A presence poll's answer is not delivered to the brain: what it is
	// for is deciding whether anybody should be talking at all, and a poll
	// that also produced remarks would be the ghost narrating your front
	// door.
	QuietWhen string `toml:"quiet_when"`
}

// KnownCapabilities are the capabilities a manifest may request. Unknown
// names are rejected at load so typos fail early and old drivers refuse
// packages built for newer ones.
var KnownCapabilities = map[string]string{
	"message":      "receive text messages from outside (haunt say, scripts)",
	"tell":         "send messages to other running ghosts",
	"native-brain": "run its brain as a native process instead of the WASM sandbox",
	"llm":          "talk with you through your language model (hermes)",
	"imagine":      "generate images with your configured image model",
	// The two screen-awareness capabilities are deliberately separate:
	// knowing which window you have open is a different order of intrusion
	// from being shown a picture of it.
	"window": "see which application and window you have focused, and its title",
	// `tools` is the outer switch on agency: it says the ghost may act
	// through the tool servers its manifest declares, and every individual
	// tool is still asked about separately the first time it is used.
	"tools":  "do things through the tool servers it declares, one tool at a time",
	"screen": "take pictures of the focused window and show them to a vision model",
}

type GhostSection struct {
	Name    string `toml:"name"`
	Version string `toml:"version"`
	Author  string `toml:"author"`
	// Capabilities the ghost requests; each needs a user grant to activate.
	Capabilities []string `toml:"capabilities"`
}

// ShellSection is the ghost's visual form: one image, or a stack of layers
// with named poses. See internal/shell.
type ShellSection = shell.Manifest

// BrainSection selects the ghost's logic. Omit it entirely — no [brain]
// section at all — and the ghost runs on hermes (hermesbrain.go), so a
// ghost can be art plus a persona with no compiler anywhere in the
// authoring path.
type BrainSection struct {
	// Wasm is the brain module path, relative to the ghost directory.
	Wasm string `toml:"wasm"`
	// Command runs the brain as a native process (argv; argv[0] relative to
	// the ghost directory) speaking the same stdio JSONL protocol. Requires
	// the native-brain capability. Mutually exclusive with Wasm.
	Command []string `toml:"command"`
}

// Builtin reports whether the ghost declares no brain of its own and so
// runs on hermes.
func (b BrainSection) Builtin() bool { return b.Wasm == "" && len(b.Command) == 0 }

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
	if hasWasm && hasCmd {
		return nil, fmt.Errorf("%s: brain.wasm and brain.command are mutually exclusive", path)
	}
	// No brain at all is the persona-only ghost, and hermes runs it. That
	// brain is a model, so a ghost asking for it without the llm capability
	// would work only in the sense that clicking it produces a quip —
	// better to say so at load than to have every sentence come back as a
	// capability error in the balloon.
	if !hasWasm && !hasCmd && !hasCapability(&m, "llm") {
		return nil, fmt.Errorf("%s: a ghost with no [brain] section runs on hermes, which needs the llm capability", path)
	}
	if hasCmd && !hasCapability(&m, "native-brain") {
		return nil, fmt.Errorf("%s: brain.command requires the native-brain capability", path)
	}
	for _, c := range m.Ghost.Capabilities {
		if _, ok := KnownCapabilities[c]; !ok {
			return nil, fmt.Errorf("%s: unknown capability %q", path, c)
		}
	}
	for _, name := range m.Hermes.BundledSkills {
		if !slices.Contains(hermesskills.Names(), name) {
			return nil, fmt.Errorf("%s: hermes.bundled_skills: %q is not one a ghost may use (%s)",
				path, name, strings.Join(hermesskills.Names(), ", "))
		}
	}
	seenTool := map[string]bool{}
	for i, t := range m.Tools {
		switch {
		case t.Name == "":
			return nil, fmt.Errorf("%s: tool server %d needs a name", path, i)
		// The name is the first half of every tool's name, so a dot in it
		// would make "notes.mail.send" ambiguous about where the server
		// ends — and that name is what the user is asked to approve.
		case strings.ContainsAny(t.Name, ". \t"):
			return nil, fmt.Errorf("%s: tool server name %q may not contain a dot or a space", path, t.Name)
		case seenTool[t.Name]:
			return nil, fmt.Errorf("%s: two tool servers are both named %q", path, t.Name)
		case len(t.Command) == 0:
			return nil, fmt.Errorf("%s: tool server %q needs a command to run", path, t.Name)
		}
		seenTool[t.Name] = true
	}
	// Declaring servers without asking for the capability is a manifest
	// that can never act, which is more likely a mistake than an intention.
	if len(m.Tools) > 0 && !hasCapability(&m, "tools") {
		return nil, fmt.Errorf("%s: [[tool]] servers are declared but the tools capability is not requested", path)
	}

	// Triggers, after the servers, because a trigger that polls a tool is
	// checked against the servers this manifest declares: a typo in a
	// scheduled call would otherwise be a thing that silently never
	// happens, discovered — if ever — in a log.
	for i, tr := range m.Triggers {
		if tr.Cron == "" || tr.Name == "" {
			return nil, fmt.Errorf("%s: trigger %d needs both cron and name", path, i)
		}
		if tr.Tool == "" {
			switch {
			case len(tr.Args) > 0:
				return nil, fmt.Errorf("%s: trigger %q has args but no tool to give them to", path, tr.Name)
			case tr.QuietWhen != "":
				return nil, fmt.Errorf("%s: trigger %q has quiet_when but no tool to ask", path, tr.Name)
			}
			continue
		}
		if err := proto.ValidToolName(tr.Tool); err != nil {
			return nil, fmt.Errorf("%s: trigger %q: %w", path, tr.Name, err)
		}
		server, _, _ := strings.Cut(tr.Tool, ".")
		if !seenTool[server] {
			return nil, fmt.Errorf("%s: trigger %q polls %s, but no [[tool]] server named %q is declared",
				path, tr.Name, tr.Tool, server)
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
