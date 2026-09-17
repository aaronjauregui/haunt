package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// TestWaitingIndicatorAndDeadline: a backend that takes its time gets a
// balloon saying so, and one that never answers is abandoned rather than
// left to hold the ghost's only AI slot until the HTTP client gives up
// minutes later.
func TestWaitingIndicatorAndDeadline(t *testing.T) {
	c := testenv.StartT(t)

	// The first request never answers; the second replies at once. That
	// pair is the whole test: the ghost must survive the first well enough
	// to serve the second.
	var calls int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read the prompt even though this fake has no use for it. A real
		// backend does, and an http.Server with an unread request body
		// cannot notice the client going away — so a handler that skips
		// this never learns it was cancelled, and hangs the test's own
		// shutdown rather than the driver's.
		io.Copy(io.Discard, r.Body)
		if atomic.AddInt32(&calls, 1) == 1 {
			<-r.Context().Done() // silence until the driver hangs up
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"here at last\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer backend.Close()

	dir := t.TempDir()
	rt := filepath.Join(dir, "run")
	cfgDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`[ai.llm]
base_url = %q
model = "fake"

[ai]
first_token_timeout = 2
`, backend.URL+"/v1")
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")
	gDir := filepath.Join(dir, "wisp")
	writeGhost(t, gDir, `[ghost]
name = "wisp"
capabilities = ["message", "llm"]

[brain]
builtin = true

[persona]
system = "You are Wisp."
`)

	const spriteX, spriteY = 100, 100
	record := filepath.Join(dir, "session.jsonl")
	cmd := exec.Command(bin, "run", "-x", "100", "-y", "100",
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
		if t.Failed() {
			t.Logf("ghost stderr:\n%s", stderr.String())
		}
	})
	waitFor(t, stdout, "READY")

	env := append(os.Environ(), "HAUNT_RUNTIME_DIR="+rt)
	say := func(text string) {
		t.Helper()
		sc := exec.Command(bin, "say", "-ghost", "wisp", text)
		sc.Env = env
		if out, err := sc.CombinedOutput(); err != nil {
			t.Fatalf("say %q: %v: %s", text, err, out)
		}
	}

	// The boot greeting has to be out of the way first, or what we see next
	// is that rather than the indicator.
	waitNoBalloon(t, c, dir, spriteX, spriteY)

	// A wait the user would notice is shown as one.
	say("are you there?")
	waitBalloon(t, c, dir, spriteX, spriteY, true)

	// And a backend that says nothing at all is given up on, with a reason
	// the brain can put in a balloon rather than a silence.
	waitRecordContains(t, record, `"type":"ai_error"`)
	if !strings.Contains(stderr.String(), "said nothing") {
		t.Errorf("the timeout was not reported as one:\n%s", stderr.String())
	}

	// The slot is free again: the next thing said is answered normally,
	// which is the part that matters — one wedged request must not deafen
	// the ghost for the rest of the session.
	say("hello again")
	waitRecordContains(t, record, "here at last")
}

// waitNoBalloon waits until nothing is beside the sprite.
func waitNoBalloon(t *testing.T, c *testenv.Compositor, dir string, x, y int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if !balloonVisible(t, c, dir, x, y) {
			return
		}
		wait100ms()
	}
	t.Fatal("a balloon never went away")
}
