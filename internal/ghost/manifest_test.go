package ghost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeManifest(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ghost.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The persona-only ghost: art, a personality, no compiler. This is the
// whole point of the built-in brain, so it is the case that must load.
func TestManifestWithoutBrainUsesTheBuiltinOne(t *testing.T) {
	dir := writeManifest(t, `[ghost]
name = "wisp"
capabilities = ["message", "llm"]

[persona]
system = "You are Wisp."
`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Brain.Builtin() {
		t.Errorf("brain = %+v, want the built-in one", m.Brain)
	}
}

// The built-in brain converses for a living. Asking for it without the llm
// capability would leave every sentence coming back as a capability error
// in the balloon, so it fails at load instead.
func TestManifestWithoutBrainNeedsLLM(t *testing.T) {
	dir := writeManifest(t, `[ghost]
name = "wisp"
capabilities = ["message"]
`)
	_, err := LoadManifest(dir)
	if err == nil || !strings.Contains(err.Error(), "llm") {
		t.Fatalf("err = %v, want a complaint about the llm capability", err)
	}
}

func TestManifestBrainKindsAreExclusive(t *testing.T) {
	dir := writeManifest(t, `[ghost]
name = "wisp"
capabilities = ["native-brain"]

[brain]
wasm = "brain.wasm"
command = ["./brain"]
`)
	if _, err := LoadManifest(dir); err == nil {
		t.Fatal("expected an error for a ghost declaring two brains")
	}
}

func TestManifestDeclaredBrainStillLoads(t *testing.T) {
	dir := writeManifest(t, `[ghost]
name = "wisp"

[brain]
wasm = "brain.wasm"
`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Brain.Builtin() {
		t.Error("a ghost with brain.wasm is not running the built-in brain")
	}
}

func TestManifestToolServer(t *testing.T) {
	dir := writeManifest(t, `[ghost]
name = "wisp"
capabilities = ["llm", "tools"]

[[tool]]
name = "notes"
command = ["mcp-notes", "--dir", "/tmp/notes"]
env = ["NOTES_TOKEN"]
allow = ["append", "search"]
`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Tools) != 1 {
		t.Fatalf("%d tool servers, want one", len(m.Tools))
	}
	tool := m.Tools[0]
	if tool.Name != "notes" || len(tool.Command) != 3 {
		t.Errorf("tool = %+v, want the server as declared", tool)
	}
	if len(tool.Env) != 1 || tool.Env[0] != "NOTES_TOKEN" {
		t.Errorf("env = %v, want the variable named and not valued", tool.Env)
	}
	if len(tool.Allow) != 2 {
		t.Errorf("allow = %v, want the manifest's own bound", tool.Allow)
	}
}

// Declaring servers without asking for the capability is a manifest that
// can never act, which is more likely a mistake than an intention.
func TestManifestToolsNeedTheCapability(t *testing.T) {
	dir := writeManifest(t, `[ghost]
name = "wisp"
capabilities = ["llm"]

[[tool]]
name = "notes"
command = ["mcp-notes"]
`)
	_, err := LoadManifest(dir)
	if err == nil || !strings.Contains(err.Error(), "tools capability") {
		t.Fatalf("err = %v, want a complaint about the tools capability", err)
	}
}

// The server's name is the first half of every tool's name, and that name
// is what the user is asked to approve — so it has to be unambiguous.
func TestManifestRejectsAmbiguousServerNames(t *testing.T) {
	for _, bad := range []string{`name = "my.notes"`, `name = "my notes"`, `name = ""`} {
		dir := writeManifest(t, `[ghost]
name = "wisp"
capabilities = ["llm", "tools"]

[[tool]]
`+bad+`
command = ["mcp-notes"]
`)
		if _, err := LoadManifest(dir); err == nil {
			t.Errorf("a tool server with %s was accepted", bad)
		}
	}
}

func TestManifestRejectsDuplicateServers(t *testing.T) {
	dir := writeManifest(t, `[ghost]
name = "wisp"
capabilities = ["llm", "tools"]

[[tool]]
name = "notes"
command = ["a"]

[[tool]]
name = "notes"
command = ["b"]
`)
	if _, err := LoadManifest(dir); err == nil {
		t.Fatal("two servers with one name were accepted")
	}
}

func TestManifestToolServerNeedsACommand(t *testing.T) {
	dir := writeManifest(t, `[ghost]
name = "wisp"
capabilities = ["llm", "tools"]

[[tool]]
name = "notes"
`)
	if _, err := LoadManifest(dir); err == nil {
		t.Fatal("a tool server with nothing to run was accepted")
	}
}
