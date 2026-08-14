package main

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// TestDevReloadsOnChange starts haunt dev in headless sway and touches the
// brain file: the ghost must restart (a second READY).
func TestDevReloadsOnChange(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()

	ghostDir := filepath.Join(dir, "whisper")
	if err := os.MkdirAll(ghostDir, 0o755); err != nil {
		t.Fatal(err)
	}
	brainWasm := filepath.Join(ghostDir, "brain.wasm")
	build(t, brainWasm, "../../examples/whisper", "GOOS=wasip1", "GOARCH=wasm")
	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")
	manifest := "[ghost]\nname = \"whisper\"\n\n[brain]\nwasm = \"brain.wasm\"\n"
	if err := os.WriteFile(filepath.Join(ghostDir, "ghost.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "dev", "-state", filepath.Join(dir, "state"), ghostDir)
	cmd.Env = c.Env()
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

	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()

	waitReady := func(which string) {
		t.Helper()
		timeout := time.After(20 * time.Second)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatalf("haunt dev exited waiting for %s READY", which)
				}
				if strings.HasPrefix(l, "READY") {
					return
				}
			case <-timeout:
				t.Fatalf("timed out waiting for %s READY", which)
			}
		}
	}

	waitReady("first")

	// Touch the brain: mtime change must trigger a reload.
	now := time.Now()
	if err := os.Chtimes(brainWasm, now, now); err != nil {
		t.Fatal(err)
	}
	waitReady("second")

	// Clean shutdown.
	cmd.Process.Signal(syscall.SIGTERM)
	if err := cmd.Wait(); err != nil {
		t.Errorf("haunt dev exited uncleanly: %v", err)
	}
}
