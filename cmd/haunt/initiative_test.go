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

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// TestInitiative: left alone, muse speaks first — and only as often as the
// user's curiosity allowance permits. The idle threshold is turned right
// down so the test does not wait two minutes for the ghost to get bored,
// but kept past bootWindow: idling inside the launch grace would be counted
// a response to being started, which is exactly what that window is for.
func TestInitiative(t *testing.T) {
	c := testenv.StartT(t)

	var chats int32
	prompts := &systemPrompts{} // reused here to capture whole requests
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&chats, 1)
		body, _ := io.ReadAll(r.Body)
		prompts.add(string(body))
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Still there?\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer backend.Close()

	dir := t.TempDir()
	rt := filepath.Join(dir, "run")
	cfgDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// One unprompted action per day: enough to prove it happens, few enough
	// to prove it stops.
	config := fmt.Sprintf(`[ai.llm]
base_url = %q
model = "fake"

[curiosity]
actions_per_day = 1
`, backend.URL+"/v1")
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")
	gDir := filepath.Join(dir, "muse")
	writeGhost(t, gDir, `[ghost]
name = "muse"
capabilities = ["message", "llm"]

[persona]
system = "You are Muse."

[brain]
wasm = "brain.wasm"
`)
	build(t, filepath.Join(gDir, "brain.wasm"), "../../examples/muse", "GOOS=wasip1", "GOARCH=wasm")

	record := filepath.Join(dir, "session.jsonl")
	stateDir := filepath.Join(dir, "state")
	cmd := exec.Command(bin, "run", "-x", "100", "-y", "100", "-idle-ms", "2500",
		"-state", stateDir, "-record", record, gDir)
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

	// Nobody touches anything: the compositor reports idle, the brain tries
	// to speak, and the driver lets it — once.
	waitRecordContains(t, record, `"type":"idle"`)
	waitRecordContains(t, record, "Still there?")
	if !strings.Contains(stderr.String(), "on the ghost's own account") {
		t.Errorf("initiative was not accounted for:\n%s", stderr.String())
	}

	// The allowance is spent. Wake the compositor and let it go idle again:
	// the ghost tries a second time and is refused.
	before := atomic.LoadInt32(&chats)
	ptr, err := c.NewPointer()
	if err != nil {
		t.Fatal(err)
	}
	if err := ptr.MoveTo(600, 400); err != nil {
		t.Fatal(err)
	}
	ptr.Close()
	waitRecordCount(t, record, `"type":"idle"`, 2)
	waitLog(t, stderr, "daily curiosity spent")
	if got := atomic.LoadInt32(&chats); got != before {
		t.Errorf("ghost kept talking past its allowance: %d chats, was %d", got, before)
	}

	// A person addressing the ghost is always answered, spent or not.
	say := exec.Command(bin, "say", "-ghost", "muse", "hello?")
	say.Env = append(os.Environ(), "HAUNT_RUNTIME_DIR="+rt)
	if out, err := say.CombinedOutput(); err != nil {
		t.Fatalf("say: %v: %s", err, out)
	}
	waitRecordCount(t, record, `"type":"ai_reply"`, 2)

	// What the ghost said unbidden is remembered as its own remark. The
	// stage direction that produced it was never said by anyone, and must
	// not turn up in history attributed to the user — a ghost that speaks
	// first would otherwise slowly invent a conversation partner.
	last := prompts.last()
	if strings.Contains(last, "Nobody has spoken") {
		t.Errorf("a self-prompt was recorded as the user's words:\n%s", last)
	}
	if !strings.Contains(last, "Still there?") {
		t.Errorf("the ghost's unprompted remark was not kept in history:\n%s", last)
	}

	// And the spend survives a restart: stopping and starting the ghost is
	// not a way to refill the allowance.
	cmd.Process.Signal(syscall.SIGTERM)
	cmd.Wait()
	chatsBeforeRestart := atomic.LoadInt32(&chats)

	record2 := filepath.Join(dir, "session2.jsonl")
	cmd2 := exec.Command(bin, "run", "-x", "100", "-y", "100", "-idle-ms", "2500",
		"-state", stateDir, "-record", record2, gDir)
	cmd2.Env = append(c.Env(), "HAUNT_RUNTIME_DIR="+rt, "HAUNT_CONFIG_DIR="+cfgDir)
	stdout2, err := cmd2.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr2 := &syncBuffer{}
	cmd2.Stderr = stderr2
	if err := cmd2.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd2.Process.Signal(syscall.SIGTERM)
		cmd2.Wait()
	})
	waitFor(t, stdout2, "READY")
	waitRecordContains(t, record2, `"type":"idle"`)
	waitLog(t, stderr2, "daily curiosity spent")
	if got := atomic.LoadInt32(&chats); got != chatsBeforeRestart {
		t.Errorf("restart refilled the allowance: %d chats, was %d", got, chatsBeforeRestart)
	}

	cmd2.Process.Signal(syscall.SIGTERM)
	if err := cmd2.Wait(); err != nil {
		t.Errorf("ghost exited uncleanly: %v", err)
	}
}

// waitLog polls the daemon's stderr for a line.
func waitLog(t *testing.T, buf *syncBuffer, want string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if strings.Contains(buf.String(), want) {
			return
		}
		wait100ms()
	}
	t.Fatalf("log never contained %q; have:\n%s", want, buf.String())
}
