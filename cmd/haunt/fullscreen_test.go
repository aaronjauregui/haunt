package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/desktop"
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

	w := desktop.New(func(s string) { t.Log(s) })
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

	backend := fakeChatBackend(t)
	defer backend.Close()

	dir := t.TempDir()
	rt := filepath.Join(dir, "run")
	cfgDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := "[ai.llm]\nbase_url = \"" + backend.URL + "/v1\"\nmodel = \"fake\"\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")
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
	cmd.Env = append(c.Env(), "HAUNT_RUNTIME_DIR="+rt, "HAUNT_CONFIG_DIR="+cfgDir)
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

// fakeChatBackend answers any chat request, so the test can tell "the ghost
// chose not to speak" from "the ghost could not".
func fakeChatBackend(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Still there?\"}}]}\n\ndata: [DONE]\n\n")
	}))
}
