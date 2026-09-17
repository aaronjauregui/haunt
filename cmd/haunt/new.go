package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// cmdNew scaffolds a ghost project. By default that is a manifest and a
// persona and nothing else — hermes is its brain, so there
// is no compiler in the authoring path and the only thing left to do is the
// art. `-brain` scaffolds the other kind: a brain in Go with a Makefile
// that builds it to WASM, for a ghost whose logic is its own.
func cmdNew(args []string) error {
	fs := flag.NewFlagSet("new", flag.ExitOnError)
	withBrain := fs.Bool("brain", false, "scaffold a Go brain compiled to WASM instead of running on hermes")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("new: exactly one ghost name expected")
	}
	name := fs.Arg(0)
	if !namePattern.MatchString(name) {
		return fmt.Errorf("new: name must be lowercase letters, digits, and hyphens, starting with a letter")
	}
	if _, err := os.Stat(name); err == nil {
		return fmt.Errorf("new: directory %q already exists", name)
	}

	var files map[string]string
	var next string
	if *withBrain {
		files, next = scriptedGhost(name)
	} else {
		files, next = personaGhost(name)
	}

	for rel, content := range files {
		path := filepath.Join(name, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("created ghost %q\n  %s\n", name, next)
	return nil
}

// personaGhost is the whole ghost as a personality: a manifest, a persona,
// one example skill, and a note about where the art goes.
func personaGhost(name string) (map[string]string, string) {
	return map[string]string{
		"ghost.toml": fmt.Sprintf(`[ghost]
name = %q
version = "0.1.0"
author = ""
# Capabilities the ghost asks the user to grant. Declaring one only means
# it may *ask*: llm is what lets it talk, "window" lets it offer to notice
# what you are working on (from its menu, and only if you say yes). Add
# "imagine" for pictures and "screen" to let it look at them.
capabilities = ["message", "llm", "window"]

[shell]
# sprite = "shell.png"   # PNG with transparency; omit for the built-in blob

# No [brain] section: this ghost runs on hermes-agent, which talks in the
# persona below, remembers what you tell it, takes on reminders, and speaks
# first now and then. Add one when you want scripted logic instead — see
# `+"`haunt new -brain`"+`.
[persona]
system = """
You are %s, a small spirit resident on this desktop.
Reply in one or two short sentences. Be specific rather than effusive.
"""

# Things it can do, rather than only say. Each server is an MCP program the
# driver runs — so the ghost never holds your credentials — and each of its
# tools is asked about the first time it is used. Add "tools" to the
# capabilities above to turn any of this on.
#
# [[tool]]
# name = "notes"                          # tools are then "notes.append", …
# command = ["mcp-server-notes", "--dir", "/home/me/notes"]
# env = ["NOTES_TOKEN"]                   # named, never stored here
# allow = ["append", "search"]            # optional: your own upper bound
`, name, name),

		"README.md": fmt.Sprintf(`# %s

A ghost: a personality and a picture.

    haunt run .

Talk to it by right-clicking it, or from a script with `+"`haunt say`"+`.
Ask it anything, tell it things to remember, or ask it to remind you;
double-click it for its menu.

Its brain is [hermes-agent](https://github.com/nousresearch/hermes-agent):
install hermes, and give the ghost's profile a model with
`+"`hermes -p haunt-%s model`"+` (or `+"`[hermes.model]`"+` in
`+"`~/.config/haunt/config.toml`"+`). The model needs a context window of
at least 64K.

## Making it yours

- **The persona** is in `+"`ghost.toml`"+`. It is the character.
- **The art** is a PNG with transparency: set `+"`sprite`"+` under [shell],
  or a stack of layers with named poses for expressions and animation.
- **Doing things** means a tool server: uncomment the `+"`[[tool]]`"+`
  block in `+"`ghost.toml`"+` and add `+"`\"tools\"`"+` to the
  capabilities. Its tools reach the model through haunt, so there is
  nothing to write.
- **Routines** are skills: a directory under `+"`skills/`"+` with a
  `+"`SKILL.md`"+` that says, in plain words, when the routine applies and
  which of the ghost's tools to use (`+"`mcp__haunt__ask`"+`,
  `+"`mcp__haunt__intend`"+` …). The model sees each skill's name and
  description (cut at 60 characters, so put the cue first), and reads the
  rest when one fits. `+"`skills/wind-down`"+`
  is an example; haunt adds its own (a morning check-in, a focus session),
  and one here with the same name replaces haunt's. Only Markdown is
  installed. Keep `+"`requires_toolsets: [mcp-haunt]`"+` in each.
- **Logic of its own** — reactions, commands, state — means a brain:
  `+"`haunt new -brain <name>`"+` scaffolds one in Go.
`, name, name),

		"skills/wind-down/SKILL.md": `---
name: wind-down
description: "End of the day: note what is left for tomorrow"
metadata:
  hermes:
    requires_toolsets: [mcp-haunt]
---

# Wind-down

Use when they say they are done for the day, or on
[haunt event] kind=session shape=late.

1. Call mcp__haunt__intentions, and mention at most one thing still open.
2. Ask with mcp__haunt__ask: "Anything to pick up tomorrow?", with the
   choices "Yes" and "No, all done".
3. On "Yes", ask mcp__haunt__ask_text what it is, then call
   mcp__haunt__intend with it and delay_ms set to reach tomorrow morning.
4. Say good night in a few words.

If they are clearly still working, reply NO_REPLY and try nothing.
`,
	}, "cd " + name + " && haunt run ."
}

// scriptedGhost is the other kind: logic of its own, in Go, sandboxed as
// WASM.
func scriptedGhost(name string) (map[string]string, string) {
	return map[string]string{
		"ghost.toml": fmt.Sprintf(`[ghost]
name = %q
version = "0.1.0"
author = ""
# Capabilities the ghost asks the user to grant:
capabilities = ["message"]

[shell]
# sprite = "shell.png"   # PNG with transparency; omit for the built-in blob

[brain]
wasm = "brain.wasm"
`, name),

		"brain/go.mod": fmt.Sprintf(`module %s-brain

go 1.22

require github.com/aaronjauregui/haunt v0.0.0

// Until Haunt is published, point at your local checkout:
replace github.com/aaronjauregui/haunt => ../../..
`, name),

		"brain/main.go": `package main

import (
	"github.com/aaronjauregui/haunt/brainkit"
)

type brain struct{}

func (b *brain) OnEvent(e brainkit.Event, emit *brainkit.Emitter) {
	switch e.Type {
	case "boot":
		emit.Say("Hello! I'm alive.")
		emit.SetTimer("hush", 4000)
	case "click":
		emit.Say("You clicked me.")
		emit.SetTimer("hush", 4000)
	case "timer":
		if e.Name == "hush" {
			emit.HideSay()
		}
	}
}

func main() {
	brainkit.Run(&brain{})
}
`,

		"Makefile": `brain.wasm: brain/main.go
	cd brain && go mod tidy && GOOS=wasip1 GOARCH=wasm go build -o ../brain.wasm .

run: brain.wasm
	haunt run .

.PHONY: run
`,
	}, "cd " + name + " && make run"
}
