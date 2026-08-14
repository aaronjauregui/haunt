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

// TestPackInstallRunPipeline exercises the full distribution path: keygen,
// pack, install (granting capabilities), then running the installed ghost by
// its library identity, with haunt say working. A second install with
// grants withheld must leave the control socket off.
func TestPackInstallRunPipeline(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()

	// Isolated config (author key), data (library), and runtime (sockets).
	env := func(cmd *exec.Cmd) {
		cmd.Env = append(c.Env(),
			"HAUNT_CONFIG_DIR="+filepath.Join(dir, "config"),
			"HAUNT_DATA_DIR="+filepath.Join(dir, "data"),
			"HAUNT_RUNTIME_DIR="+filepath.Join(dir, "run"),
		)
	}

	ghostDir := filepath.Join(dir, "whisper")
	if err := os.MkdirAll(ghostDir, 0o755); err != nil {
		t.Fatal(err)
	}
	build(t, filepath.Join(ghostDir, "brain.wasm"), "../../examples/whisper", "GOOS=wasip1", "GOARCH=wasm")
	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")
	manifest := `[ghost]
name = "whisper"
version = "0.2.0"
capabilities = ["message"]

[brain]
wasm = "brain.wasm"
`
	if err := os.WriteFile(filepath.Join(ghostDir, "ghost.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, args...)
		env(cmd)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("haunt %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}

	run("keygen")
	pkgFile := filepath.Join(dir, "whisper.haunt")
	run("pack", "-o", pkgFile, ghostDir)
	out := run("install", "-yes", pkgFile)
	if !strings.Contains(out, "installed whisper 0.2.0") {
		t.Fatalf("install output: %s", out)
	}
	if !strings.Contains(run("ls"), "* whisper 0.2.0") {
		t.Fatalf("ls output: %s", run("ls"))
	}

	// Run by library identity (no arg: current ghost).
	daemon := exec.Command(bin, "run", "-x", "100", "-y", "100")
	env(daemon)
	stdout, err := daemon.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	daemon.Stderr = os.Stderr
	if err := daemon.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		daemon.Process.Signal(syscall.SIGTERM)
		daemon.Wait()
	})
	waitFor(t, stdout, "READY")

	// Granted message capability: say works.
	run("say", "hello installed ghost")

	daemon.Process.Signal(syscall.SIGTERM)
	if err := daemon.Wait(); err != nil {
		t.Errorf("daemon exited uncleanly: %v", err)
	}

	// Reinstall while withholding grants: update keeps running but the
	// control socket must be off. (Simulate withholding by rewriting
	// grants.toml with no capabilities.)
	grantsPath := filepath.Join(dir, "data", "ghosts", "whisper", "grants.toml")
	g, err := os.ReadFile(grantsPath)
	if err != nil {
		t.Fatal(err)
	}
	stripped := strings.Replace(string(g), `"message"`, "", 1)
	if err := os.WriteFile(grantsPath, []byte(stripped), 0o644); err != nil {
		t.Fatal(err)
	}

	daemon2 := exec.Command(bin, "run")
	env(daemon2)
	stdout2, err := daemon2.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	daemon2.Stderr = os.Stderr
	if err := daemon2.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		daemon2.Process.Signal(syscall.SIGTERM)
		daemon2.Wait()
	})
	waitFor(t, stdout2, "READY")

	sayCmd := exec.Command(bin, "say", "should not arrive")
	env(sayCmd)
	if out, err := sayCmd.CombinedOutput(); err == nil {
		t.Fatalf("say succeeded without message grant: %s", out)
	}

	daemon2.Process.Signal(syscall.SIGTERM)
	if err := daemon2.Wait(); err != nil {
		t.Errorf("daemon2 exited uncleanly: %v", err)
	}
}

func waitFor(t *testing.T, r interface{ Read([]byte) (int, error) }, prefix string) {
	t.Helper()
	found := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), prefix) {
				found <- true
				return
			}
		}
		found <- false
	}()
	select {
	case ok := <-found:
		if !ok {
			t.Fatalf("stream ended before %q", prefix)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("timed out waiting for %q", prefix)
	}
}
