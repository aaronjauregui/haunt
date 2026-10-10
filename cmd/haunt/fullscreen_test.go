package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/desktop"
	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/model/fake"
	"github.com/aaronjauregui/haunt/internal/testenv"
)

// TestFullscreenDetected: the whole rule rests on being able to tell, so
// this drives a real window into fullscreen and asks.
func TestFullscreenDetected(t *testing.T) {
	c := testenv.StartT(t)
	if _, err := exec.LookPath("foot"); err != nil {
		t.Skip("no terminal available to fill the screen with")
	}

	// The watcher reads SWAYSOCK from its environment, so point this
	// process at the harness compositor.
	for _, kv := range c.Env() {
		if name, value, ok := strings.Cut(kv, "="); ok &&
			(name == "SWAYSOCK" || name == "WAYLAND_DISPLAY") {
			t.Setenv(name, value)
		}
	}

	w := desktop.New(hlog.Discard())
	if w.Mechanism() != "sway" {
		t.Fatalf("mechanism = %q, want sway", w.Mechanism())
	}
	if w.Fullscreen() {
		t.Error("an empty desktop reports something fullscreen")
	}

	if out, err := c.Swaymsg("exec", "foot"); err != nil {
		t.Skipf("cannot spawn a window: %v: %s", err, out)
	}
	waitForWindow(t, c)
	if _, err := c.Swaymsg("fullscreen", "enable"); err != nil {
		t.Fatalf("fullscreen: %v", err)
	}

	if !eventually(3*time.Second, func() bool {
		w2 := desktop.New(nil) // fresh, to dodge the cache
		return w2.Fullscreen()
	}) {
		t.Error("a fullscreen window was not detected")
	}

	// And leaving fullscreen is noticed too.
	if _, err := c.Swaymsg("fullscreen", "disable"); err != nil {
		t.Fatal(err)
	}
	if !eventually(3*time.Second, func() bool {
		return !desktop.New(nil).Fullscreen()
	}) {
		t.Error("still reporting fullscreen after leaving it")
	}
}

// TestGhostKeepsQuietOverFullscreen: with a film or a game in front of
// them, a person should not be interrupted by their desktop pet.
func TestGhostKeepsQuietOverFullscreen(t *testing.T) {
	c := testenv.StartT(t)
	if _, err := exec.LookPath("foot"); err != nil {
		t.Skip("no terminal available to fill the screen with")
	}

	dir := t.TempDir()
	// A model that would speak, so staying quiet is the driver's doing.
	rig := newBrainRig(t, dir, rules(
		says("kind=idle", "Still there?"),
		[]fake.Rule{silentOn("[haunt event]")},
	), "")
	bin := rig.Bin
	gDir := filepath.Join(dir, "wisp")
	writeGhost(t, gDir, `[ghost]
name = "wisp"
capabilities = ["message", "llm"]

[persona]
system = "You are Wisp."
`)

	// Fill the screen before the ghost even starts.
	if out, err := c.Swaymsg("exec", "foot"); err != nil {
		t.Skipf("cannot spawn a window: %v: %s", err, out)
	}
	waitForWindow(t, c)
	if _, err := c.Swaymsg("fullscreen", "enable"); err != nil {
		t.Fatal(err)
	}

	record := filepath.Join(dir, "session.jsonl")
	cmd := exec.Command(bin, "run", "-x", "100", "-y", "100", "-idle-ms", "2500",
		"-state", filepath.Join(dir, "state"), "-record", record, gDir)
	cmd.Env = append(c.Env(), rig.env()...)
	// So that "the ghost stayed quiet" is a real assertion rather than one
	// that passes because the line was filtered out.
	cmd.Env = append(cmd.Env, "HAUNT_DEBUG=curiosity")
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
	})
	waitFor(t, stdout, "READY")

	// Left alone it would speak up; over a fullscreen window it must not.
	waitRecordContains(t, record, `"type":"idle"`)
	waitLog(t, stderr, "something is fullscreen")
	if strings.Contains(stderr.String(), "on the ghost's own account") {
		t.Errorf("the ghost spoke over a fullscreen window:\n%s", stderr.String())
	}
	if got := fakeEntries(rig.FakeLog, turnsAbout("kind=idle")); len(got) != 0 {
		t.Errorf("the model was woken over a fullscreen window: %+v", got)
	}
}

// waitForWindow blocks until sway has a real window mapped.
func waitForWindow(t *testing.T, c *testenv.Compositor) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if out, err := c.Swaymsg("-t", "get_tree"); err == nil &&
			strings.Contains(string(out), `"app_id": "foot"`) {
			return
		}
		wait100ms()
	}
	t.Skip("no window appeared")
}

func eventually(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false
}
