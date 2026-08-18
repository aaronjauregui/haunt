package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// TestSessionFactsFromRealWindows: the middle layer, end to end. A brain
// given the raw stream is told which window has focus every three seconds
// and is expected to be interesting about it; what it needs instead is the
// shape over time. Here the shape is a dwell — a stretch in one window long
// enough to be worth remarking on — produced from a real compositor's real
// focus reports rather than a synthetic clock.
func TestSessionFactsFromRealWindows(t *testing.T) {
	c := testenv.StartT(t)
	if _, err := exec.LookPath("foot"); err != nil {
		t.Skip("no terminal to focus")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")

	gDir := filepath.Join(dir, "watcher")
	writeGhost(t, gDir, `[ghost]
name = "watcher"
capabilities = ["window"]

[brain]
wasm = "brain.wasm"
`)
	build(t, filepath.Join(gDir, "brain.wasm"), "../../examples/whisper", "GOOS=wasip1", "GOARCH=wasm")

	// A dwell of a few seconds rather than the twenty-five minutes a real
	// desktop wants: the threshold is the user's dial, which makes this
	// testable without pretending about time.
	cfgDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"),
		[]byte("[awareness]\ndwell = \"6s\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	record := filepath.Join(dir, "session.jsonl")
	cmd := exec.Command(bin, "run", "-x", "20", "-y", "20",
		"-state", filepath.Join(dir, "state"), "-record", record, gDir)
	cmd.Env = append(c.Env(), "HAUNT_RUNTIME_DIR="+filepath.Join(dir, "run"),
		"HAUNT_CONFIG_DIR="+cfgDir, "HAUNT_DEBUG=screen")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := &syncBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGTERM)
		cmd.Wait()
		if t.Failed() {
			t.Logf("ghost stderr:\n%s", stderr.String())
		}
	})
	waitFor(t, stdout, "READY")

	if out, err := c.Swaymsg("exec", "foot"); err != nil {
		t.Skipf("cannot spawn a window: %v: %s", err, out)
	}
	waitForWindow(t, c)

	// The raw event comes first, as it always did.
	waitRecordContains(t, record, `"type":"window"`)

	// Then, after sitting in it, the fact about it.
	waitRecordContainsFor(t, record, `"type":"session"`, 30*time.Second)
	waitRecordContains(t, record, `"kind":"dwell"`)
	data, _ := os.ReadFile(record)
	if !strings.Contains(string(data), `"app":"foot"`) {
		t.Errorf("the dwell does not say what it was about:\n%s", data)
	}
	// The summary is the form a brain hands to a model, so it has to read
	// like something a person would say.
	if !strings.Contains(string(data), "minutes in foot") && !strings.Contains(string(data), "0 minutes") {
		t.Logf("summary text:\n%s", data)
	}
	if !strings.Contains(stderr.String(), "noticed:") {
		t.Errorf("nothing was logged about what it noticed:\n%s", stderr.String())
	}
}

// The ignore list applies before aggregation, not after: an application
// nobody wants watched must not turn up in a duration statistic either,
// even unnamed. Checked here through the driver, because that ordering is
// the driver's to get right.
func TestIgnoredWindowsLeaveNoTrace(t *testing.T) {
	c := testenv.StartT(t)
	if _, err := exec.LookPath("foot"); err != nil {
		t.Skip("no terminal to focus")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")

	gDir := filepath.Join(dir, "watcher")
	writeGhost(t, gDir, `[ghost]
name = "watcher"
capabilities = ["window"]

[brain]
wasm = "brain.wasm"
`)
	build(t, filepath.Join(gDir, "brain.wasm"), "../../examples/whisper", "GOOS=wasip1", "GOARCH=wasm")

	cfgDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// foot is the only window there will be, and it is on the never-watch
	// list.
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"),
		[]byte("[awareness]\nignore = [\"foot\"]\ndwell = \"4s\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	record := filepath.Join(dir, "session.jsonl")
	cmd := exec.Command(bin, "run", "-x", "20", "-y", "20",
		"-state", filepath.Join(dir, "state"), "-record", record, gDir)
	cmd.Env = append(c.Env(), "HAUNT_RUNTIME_DIR="+filepath.Join(dir, "run"),
		"HAUNT_CONFIG_DIR="+cfgDir)
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

	if out, err := c.Swaymsg("exec", "foot"); err != nil {
		t.Skipf("cannot spawn a window: %v: %s", err, out)
	}
	waitForWindow(t, c)
	time.Sleep(12 * time.Second) // three times the dwell threshold

	data, _ := os.ReadFile(record)
	if strings.Contains(string(data), `"type":"session"`) {
		t.Errorf("an ignored application produced a session fact:\n%s", data)
	}
	if strings.Contains(string(data), "foot") {
		t.Errorf("an ignored application was named at all:\n%s", data)
	}
}

// waitRecordContainsFor is waitRecordContains with a longer patience, for
// facts that are made of time passing.
func waitRecordContainsFor(t *testing.T, path, want string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("recording never contained %q; have:\n%s", want, data)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
