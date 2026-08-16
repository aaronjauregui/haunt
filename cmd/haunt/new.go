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
// persona and nothing else — the driver's built-in brain runs it, so there
// is no compiler in the authoring path and the only thing left to do is the
// art. `-brain` scaffolds the other kind: a brain in Go with a Makefile
// that builds it to WASM, for a ghost whose logic is its own.
func cmdNew(args []string) error {
	fs := flag.NewFlagSet("new", flag.ExitOnError)
	withBrain := fs.Bool("brain", false, "scaffold a Go brain compiled to WASM instead of using the built-in brain")
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
// and a note about where the art goes.
func personaGhost(name string) (map[string]string, string) {
	return map[string]string{
		"ghost.toml": fmt.Sprintf(`[ghost]
name = %q
version = "0.1.0"
author = ""
# Capabilities the ghost asks the user to grant. llm is what lets it talk;
# add "imagine" for pictures, "window"/"screen" to let it notice what you
# are doing.
capabilities = ["message", "llm"]

[shell]
# sprite = "shell.png"   # PNG with transparency; omit for the built-in blob

# No [brain] section: this ghost runs on the driver's built-in brain, which
# chats in the persona below, draws when asked, keeps a journal, and speaks
# first now and then. Add one when you want logic of your own — see
# `+"`haunt new -brain`"+`.
[persona]
system = """
You are %s, a small spirit resident on this desktop.
Reply in one or two short sentences. Be specific rather than effusive.
"""
`, name, name),

		"README.md": fmt.Sprintf(`# %s

A ghost: a personality and a picture.

    haunt run .

Talk to it by right-clicking it, or from a script with `+"`haunt say`"+`.
It answers to `+"`help`"+`, `+"`draw <something>`"+`, `+"`remember <fact>`"+`
and `+"`look`"+` — the rest is conversation.

Chatting needs an LLM in `+"`~/.config/haunt/config.toml`"+` ([ai.llm]);
any OpenAI-compatible endpoint will do, including a local Ollama.

## Making it yours

- **The persona** is in `+"`ghost.toml`"+`. It is the character.
- **The art** is a PNG with transparency: set `+"`sprite`"+` under [shell],
  or a stack of layers with named poses for expressions and animation.
- **Logic of its own** — reactions, commands, state — means a brain:
  `+"`haunt new -brain <name>`"+` scaffolds one in Go.
`, name),
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
