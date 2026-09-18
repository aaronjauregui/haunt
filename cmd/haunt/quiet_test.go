package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// TestQuietHoursSuppressBalloons shushes, boots a ghost, and the greeting
// balloon must not appear even though the boot event flows.
func TestQuietHoursSuppressBalloons(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	rt := filepath.Join(dir, "run")
	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")

	gDir := filepath.Join(dir, "whisper")
	writeGhost(t, gDir, `[ghost]
name = "whisper"
capabilities = ["message"]

[brain]
wasm = "brain.wasm"
`)
	build(t, filepath.Join(gDir, "brain.wasm"), "../../examples/whisper", "GOOS=wasip1", "GOARCH=wasm")

	shush := exec.Command(bin, "shush", "1h")
	shush.Env = append(os.Environ(), "HAUNT_RUNTIME_DIR="+rt)
	if out, err := shush.CombinedOutput(); err != nil {
		t.Fatalf("shush: %v: %s", err, out)
	}

	record := filepath.Join(dir, "whisper.jsonl")
	g := startGhost(t, c, bin, gDir, filepath.Join(dir, "state"), record, rt, 100, 100)

	// The boot event flows, but no balloon may appear.
	waitRecordContains(t, record, `"type":"boot"`)
	time.Sleep(700 * time.Millisecond)
	waitBalloon(t, c, dir, 100, 100, false)

	// Unshush, then a message shows a balloon again.
	unshush := exec.Command(bin, "unshush")
	unshush.Env = append(os.Environ(), "HAUNT_RUNTIME_DIR="+rt)
	if out, err := unshush.CombinedOutput(); err != nil {
		t.Fatalf("unshush: %v: %s", err, out)
	}
	say := exec.Command(bin, "say", "-ghost", "whisper", "hello again")
	say.Env = append(os.Environ(), "HAUNT_RUNTIME_DIR="+rt)
	if out, err := say.CombinedOutput(); err != nil {
		t.Fatalf("say: %v: %s", err, out)
	}
	waitBalloon(t, c, dir, 100, 100, true)

	stopGhost(t, g)
}
