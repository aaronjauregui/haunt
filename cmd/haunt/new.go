package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// cmdNew scaffolds a working ghost project: manifest, a brain in Go, and a
// Makefile that builds it to WASM.
func cmdNew(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("new: exactly one ghost name expected")
	}
	name := args[0]
	if !namePattern.MatchString(name) {
		return fmt.Errorf("new: name must be lowercase letters, digits, and hyphens, starting with a letter")
	}
	if _, err := os.Stat(name); err == nil {
		return fmt.Errorf("new: directory %q already exists", name)
	}

	files := map[string]string{
		"ghost.toml": fmt.Sprintf(`[ghost]
name = %q
version = "0.1.0"
author = ""

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
	fmt.Printf("created ghost %q\n  cd %s && make run\n", name, name)
	return nil
}
