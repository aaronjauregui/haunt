package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// startGhost launches `haunt run` for a ghost dir with an isolated runtime
// dir, waiting for READY.
func startGhost(t *testing.T, c *testenv.Compositor, bin, ghostDir, stateDir, recordPath, runtimeDir string, x, y int) *exec.Cmd {
	t.Helper()
	args := []string{"run", "-x", fmt.Sprint(x), "-y", fmt.Sprint(y), "-state", stateDir}
	if recordPath != "" {
		args = append(args, "-record", recordPath)
	}
	args = append(args, ghostDir)
	cmd := exec.Command(bin, args...)
	cmd.Env = append(c.Env(), "HAUNT_RUNTIME_DIR="+runtimeDir)
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
	return cmd
}

func stopGhost(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	cmd.Process.Signal(syscall.SIGTERM)
	if err := cmd.Wait(); err != nil {
		t.Errorf("ghost exited uncleanly: %v", err)
	}
}

func writeGhost(t *testing.T, dir, manifest string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ghost.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

// waitRecordContains polls a session recording for a substring.
func waitRecordContains(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("recording %s never contained %q; have:\n%s", path, want, data)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestCrossGhostTell runs whisper and a caller ghost; the caller's tell
// intent must arrive at whisper as a message with From identity.
func TestCrossGhostTell(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	rt := filepath.Join(dir, "run")
	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")

	whisperDir := filepath.Join(dir, "whisper")
	writeGhost(t, whisperDir, `[ghost]
name = "whisper"
capabilities = ["message"]

[brain]
wasm = "brain.wasm"
`)
	build(t, filepath.Join(whisperDir, "brain.wasm"), "../../examples/whisper", "GOOS=wasip1", "GOARCH=wasm")

	callerDir := filepath.Join(dir, "caller")
	writeGhost(t, callerDir, `[ghost]
name = "caller"
capabilities = ["tell"]

[brain]
wasm = "brain.wasm"
`)
	build(t, filepath.Join(callerDir, "brain.wasm"), "./testdata/caller", "GOOS=wasip1", "GOARCH=wasm")

	record := filepath.Join(dir, "whisper.jsonl")
	w := startGhost(t, c, bin, whisperDir, filepath.Join(dir, "ws"), record, rt, 100, 100)
	cl := startGhost(t, c, bin, callerDir, filepath.Join(dir, "cs"), "", rt, 500, 100)

	waitRecordContains(t, record, `"ping from caller"`)
	waitRecordContains(t, record, `"from":"caller"`)

	stopGhost(t, cl)
	stopGhost(t, w)
}

// TestNativeBrainAndCron runs a ghost with a native-process brain and an
// @every cron trigger; both must function (dev run auto-grants).
func TestNativeBrainAndCron(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	rt := filepath.Join(dir, "run")
	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")

	gDir := filepath.Join(dir, "native")
	writeGhost(t, gDir, `[ghost]
name = "native"
capabilities = ["message", "native-brain"]

[brain]
command = ["brain-native"]

[[trigger]]
cron = "@every 1s"
name = "pulse"
`)
	// The whisper brain compiled natively speaks the same protocol.
	build(t, filepath.Join(gDir, "brain-native"), "../../examples/whisper")

	record := filepath.Join(dir, "native.jsonl")
	g := startGhost(t, c, bin, gDir, filepath.Join(dir, "state"), record, rt, 100, 100)

	// Boot reached the native brain: its greeting balloon appears.
	waitBalloon(t, c, dir, 100, 100, true)
	// Cron trigger flows through the bus.
	waitRecordContains(t, record, `"type":"cron"`)
	waitRecordContains(t, record, `"name":"pulse"`)
	// Messaging works against a native brain too.
	say := exec.Command(bin, "say", "-ghost", "native", "over the wire")
	say.Env = append(c.Env(), "HAUNT_RUNTIME_DIR="+rt)
	if out, err := say.CombinedOutput(); err != nil {
		t.Fatalf("say: %v: %s", err, out)
	}
	waitRecordContains(t, record, `"over the wire"`)

	stopGhost(t, g)
}

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
