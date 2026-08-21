package main

import (
	"encoding/json"
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

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// An intention is the one piece of a ghost's state whose whole purpose is
// to outlive the process, so the only test that means anything is one that
// actually stops the ghost and starts it again.

// TestAnIntentionSurvivesARestart: the ghost is asked to remember to do
// something, is killed, is started again, and hands the errand back.
func TestAnIntentionSurvivesARestart(t *testing.T) {
	c := testenv.StartT(t)

	// A backend that echoes the direction it was given, so the test can see
	// which errand came back rather than only that something did.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		last := req.Messages[len(req.Messages)-1].Content
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		chunk, _ := json.Marshal(map[string]any{"choices": []map[string]any{
			{"delta": map[string]string{"content": "reminding you: " + last}},
		}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
		fl.Flush()
	}))
	defer backend.Close()

	dir := t.TempDir()
	rt := filepath.Join(dir, "run")
	cfgDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf("[ai.llm]\nbase_url = %q\nmodel = \"fake\"\n", backend.URL+"/v1")
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfg), 0o644); err != nil {
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
	state := filepath.Join(dir, "state")
	env := append(os.Environ(), "HAUNT_RUNTIME_DIR="+rt)

	// Start the ghost, ask it to remember something, and stop it.
	record := filepath.Join(dir, "first.jsonl")
	stop := startWisp(t, c, bin, gDir, state, rt, cfgDir, record)
	say(t, bin, env, "remind me in 2 seconds to check the build")

	// The recording is the event bus, and an intention is an *intent* — it
	// never appears there. What a person can see is the CLI, so that is
	// what this waits on, which also checks the one path they have to find
	// out what their ghost is carrying.
	waitIntentions(t, bin, state, rt, "check the build")
	stop()

	// Gone. The errand is on disk and nothing is running to fire it; wait
	// past its moment so the restart is genuinely picking up something
	// overdue.
	time.Sleep(3 * time.Second)
	if got := readIntentions(t, bin, state, rt); !strings.Contains(got, "check the build") {
		t.Fatalf("the errand did not outlive the ghost:\n%s", got)
	}

	// Started again, it hands the errand back of its own accord.
	second := filepath.Join(dir, "second.jsonl")
	stop2 := startWisp(t, c, bin, gDir, state, rt, cfgDir, second)
	defer stop2()

	waitRecordContainsFor(t, second, `"type":"intention"`, 60*time.Second)
	waitRecordContainsFor(t, second, "check the build", 60*time.Second)

	// And having said it, it stops carrying it.
	deadline := time.Now().Add(30 * time.Second)
	for {
		got := readIntentions(t, bin, state, rt)
		if !strings.Contains(got, "check the build") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("it is still carrying an errand it has handed back:\n%s", got)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// waitIntentions polls the CLI until the ghost admits to carrying want.
func waitIntentions(t *testing.T, bin, state, rt, want string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		got := readIntentions(t, bin, state, rt)
		if strings.Contains(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("it never took on %q; carrying:\n%s", want, got)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// startWisp runs the ghost until the returned function is called.
func startWisp(t *testing.T, c *testenv.Compositor, bin, gDir, state, rt, cfgDir, record string) func() {
	t.Helper()
	cmd := exec.Command(bin, "run", "-x", "100", "-y", "100",
		"-state", state, "-record", record, gDir)
	cmd.Env = append(c.Env(), "HAUNT_RUNTIME_DIR="+rt, "HAUNT_CONFIG_DIR="+cfgDir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, stdout, "READY")
	stopped := false
	return func() {
		if stopped {
			return
		}
		stopped = true
		cmd.Process.Signal(syscall.SIGTERM)
		cmd.Wait()
	}
}

func say(t *testing.T, bin string, env []string, text string) {
	t.Helper()
	sc := exec.Command(bin, "say", "-ghost", "wisp", text)
	sc.Env = env
	if out, err := sc.CombinedOutput(); err != nil {
		t.Fatalf("say %q: %v: %s", text, err, out)
	}
}

// readIntentions asks the CLI what the ghost is still meaning to do, which
// is also the only way a person can find out.
func readIntentions(t *testing.T, bin, state, rt string) string {
	t.Helper()
	cmd := exec.Command(bin, "journal", "-intentions", "-state", state)
	cmd.Env = append(os.Environ(), "HAUNT_RUNTIME_DIR="+rt)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("journal -intentions: %v: %s", err, out)
	}
	return string(out)
}
