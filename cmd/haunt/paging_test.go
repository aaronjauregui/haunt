package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// TestPagingSwallowsClicksThenReleasesThem: while a long reply is being
// read, clicking the ghost turns the page rather than poking it; once the
// last page is gone, clicks reach the brain again.
func TestPagingSwallowsClicksThenReleasesThem(t *testing.T) {
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

	const spriteX, spriteY = 200, 200
	record := filepath.Join(dir, "session.jsonl")
	cmd := exec.Command(bin, "run", "-x", "200", "-y", "200",
		"-state", filepath.Join(dir, "state"), "-record", record, gDir)
	cmd.Env = append(c.Env(), "HAUNT_RUNTIME_DIR="+rt)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGTERM)
		cmd.Wait()
	})
	waitFor(t, stdout, "READY")

	para := strings.Repeat("The ghost considered the matter at some length, and found it wanting. ", 12)
	say := exec.Command(bin, "say", "-ghost", "whisper", para+"\n\n"+para)
	say.Env = append(os.Environ(), "HAUNT_RUNTIME_DIR="+rt)
	if out, err := say.CombinedOutput(); err != nil {
		t.Fatalf("say: %v: %s", err, out)
	}
	waitBalloon(t, c, dir, spriteX, spriteY, true)

	ptr, err := c.NewPointer()
	if err != nil {
		t.Fatal(err)
	}
	defer ptr.Close()
	click := func() {
		t.Helper()
		if err := ptr.MoveTo(spriteX+80, spriteY+80); err != nil {
			t.Fatal(err)
		}
		if err := ptr.Press(); err != nil {
			t.Fatal(err)
		}
		if err := ptr.Release(); err != nil {
			t.Fatal(err)
		}
	}

	// The first click turns a page: the brain hears nothing.
	click()
	wait100ms()
	if data, _ := os.ReadFile(record); strings.Contains(string(data), `"type":"click"`) {
		t.Error("a page turn reached the brain as a click")
	}

	// Keep clicking until the balloon is gone; then the next click is a
	// poke like any other.
	for i := 0; i < 12; i++ {
		if !balloonVisible(t, c, dir, spriteX, spriteY) {
			break
		}
		click()
		wait100ms()
	}
	if balloonVisible(t, c, dir, spriteX, spriteY) {
		t.Fatal("the balloon never ran out of pages")
	}
	click()
	waitRecordContains(t, record, `"type":"click"`)
}
