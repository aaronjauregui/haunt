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
