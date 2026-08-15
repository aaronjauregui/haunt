package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

const ollamaURL = "http://localhost:11434"

// ollamaModel returns a locally pulled model that can hold a conversation,
// or "" when Ollama is not running or has none. The capability check is not
// pedantry: pulling an embedding model changes what is listed first, and
// picking it gets a "does not support chat" rejection instead of a reply.
// HAUNT_TEST_OLLAMA_MODEL overrides the pick.
func ollamaModel(t *testing.T) string {
	t.Helper()
	if m := os.Getenv("HAUNT_TEST_OLLAMA_MODEL"); m != "" {
		return m
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(ollamaURL + "/api/tags")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var out struct {
		Models []struct {
			Name         string   `json:"name"`
			Capabilities []string `json:"capabilities"`
		} `json:"models"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return ""
	}
	for _, m := range out.Models {
		for _, c := range m.Capabilities {
			if c == "completion" {
				return m.Name
			}
		}
		// Older Ollama builds report no capabilities; fall back to the
		// naming convention embedding models follow.
		if len(m.Capabilities) == 0 && !strings.Contains(m.Name, "embed") {
			return m.Name
		}
	}
	return ""
}

// ollamaAnswers checks that the server actually completes a request, not
// merely that it is listening. A wedged Ollama keeps serving /api/tags
// while inference hangs forever, and a live test that only checks the
// former spends its whole timeout before reporting a failure that belongs
// to the machine rather than to the code.
func ollamaAnswers(t *testing.T, model string) bool {
	t.Helper()
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":false}`, model)
	client := &http.Client{Timeout: 25 * time.Second}
	resp, err := client.Post(ollamaURL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Logf("ollama is listening but not completing requests: %v", err)
		return false
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

// TestOllamaGhost runs muse against a real local Ollama server: a live LLM
// reply must stream back and reach the session record. Skips when Ollama is
// unavailable, so CI without it stays green.
func TestOllamaGhost(t *testing.T) {
	model := ollamaModel(t)
	if model == "" {
		t.Skip("no local Ollama server with a model; skipping live LLM test")
	}
	if !ollamaAnswers(t, model) {
		t.Skip("local Ollama is not completing requests; skipping live LLM test")
	}
	c := testenv.StartT(t)

	dir := t.TempDir()
	rt := filepath.Join(dir, "run")
	cfgDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("[ai.llm]\nbase_url = %q\nmodel = %q\n", ollamaURL+"/v1", model)
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
system = "You are Muse, a tiny desktop spirit. Always reply with a single short sentence."

[brain]
wasm = "brain.wasm"
`)
	build(t, filepath.Join(gDir, "brain.wasm"), "../../examples/muse", "GOOS=wasip1", "GOARCH=wasm")

	record := filepath.Join(dir, "session.jsonl")
	cmd := exec.Command(bin, "run", "-x", "100", "-y", "100",
		"-state", filepath.Join(dir, "state"), "-record", record, gDir)
	cmd.Env = append(c.Env(), "HAUNT_RUNTIME_DIR="+rt, "HAUNT_CONFIG_DIR="+cfgDir)
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

	say := exec.Command(bin, "say", "-ghost", "muse", "say hello in one short sentence")
	say.Env = append(os.Environ(), "HAUNT_RUNTIME_DIR="+rt)
	if out, err := say.CombinedOutput(); err != nil {
		t.Fatalf("say: %v: %s", err, out)
	}

	// A real model can be slow on first load; allow a generous window for
	// the reply event, then confirm the balloon made it to the screen.
	waitRecordContainsSlow(t, record, `"type":"ai_reply"`, 120*time.Second, model)
	waitBalloon(t, c, dir, 100, 100, true)

	cmd.Process.Signal(syscall.SIGTERM)
	if err := cmd.Wait(); err != nil {
		t.Errorf("ghost exited uncleanly: %v", err)
	}
}

// waitRecordContainsSlow is waitRecordContains with a custom deadline, and
// with a check on the way out: a live test can only conclude anything if
// its dependency stayed up. If nothing arrived and Ollama has stopped
// answering — which it does under load on some setups, while still serving
// its API — that is the machine's problem and the test skips rather than
// blaming the code.
func waitRecordContainsSlow(t *testing.T, path, want string, timeout time.Duration, model string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), want) {
			return
		}
		if time.Now().After(deadline) {
			if !ollamaAnswers(t, model) {
				t.Skipf("ollama stopped completing requests mid-test; skipping rather than reporting a failure it caused")
			}
			t.Fatalf("recording never contained %q; have:\n%s", want, data)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// ollamaHasModel reports whether a named model is pulled locally.
func ollamaHasModel(name string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(ollamaURL + "/v1/models")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return false
	}
	for _, m := range out.Data {
		if strings.HasPrefix(m.ID, name) {
			return true
		}
	}
	return false
}

// syncBuffer collects the daemon's stderr while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	os.Stderr.Write(p)
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestOllamaMemory exercises long-term memory against real models: a fact
// stated early leaves the history window, and comes back into the system
// prompt when the subject returns. Prompt tracing is how the test sees the
// prompt at all, which is the same way a person would. Skips unless both an
// LLM and nomic-embed-text are pulled locally.
func TestOllamaMemory(t *testing.T) {
	model := ollamaModel(t)
	if model == "" {
		t.Skip("no local Ollama server with a model; skipping live memory test")
	}
	const embedModel = "nomic-embed-text"
	if !ollamaHasModel(embedModel) {
		t.Skipf("no %s model pulled; skipping live memory test", embedModel)
	}
	if !ollamaAnswers(t, model) {
		t.Skip("local Ollama is not completing requests; skipping live memory test")
	}
	c := testenv.StartT(t)

	dir := t.TempDir()
	rt := filepath.Join(dir, "run")
	cfgDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`[ai.llm]
base_url = %q
model = %q

[ai.embed]
model = %q

[ai]
log_prompts = true
`, ollamaURL+"/v1", model, embedModel)
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
system = "You are Muse, a tiny desktop spirit. Always reply with a single short sentence."
history_turns = 1

[brain]
wasm = "brain.wasm"
`)
	build(t, filepath.Join(gDir, "brain.wasm"), "../../examples/muse", "GOOS=wasip1", "GOARCH=wasm")

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
	})
	waitFor(t, stdout, "READY")

	replies := 0
	say := func(text string) {
		t.Helper()
		sc := exec.Command(bin, "say", "-ghost", "muse", text)
		sc.Env = append(os.Environ(), "HAUNT_RUNTIME_DIR="+rt)
		if out, err := sc.CombinedOutput(); err != nil {
			t.Fatalf("say %q: %v: %s", text, err, out)
		}
		replies++
		waitRecordCountSlow(t, record, `"type":"ai_reply"`, replies, 120*time.Second, model)
	}

	say("I drink oolong tea every morning")
	say("my socks are soaking wet")
	say("the compiler is angry at me again")
	say("what tea should I buy")

	// The last traced chat request is the one that matters: two exchanges
	// have pushed the tea remark out of a one-turn history window.
	traced := stderr.String()
	lastChat := strings.LastIndex(traced, "haunt: ai: chat →")
	if lastChat < 0 {
		t.Fatalf("no chat trace; log_prompts not working:\n%s", traced)
	}
	final := traced[lastChat:]
	if !strings.Contains(final, "Things this reminds you of") {
		t.Errorf("real embeddings recalled nothing for a tea question:\n%s", final)
	}
	if !strings.Contains(strings.ToLower(final), "oolong") {
		t.Errorf("recall missed the tea memory:\n%s", final)
	}

	cmd.Process.Signal(syscall.SIGTERM)
	if err := cmd.Wait(); err != nil {
		t.Errorf("ghost exited uncleanly: %v", err)
	}
}

// waitRecordCountSlow is waitRecordCount with a timeout sized for real
// models, which can stall on first load.
func waitRecordCountSlow(t *testing.T, path, s string, want int, timeout time.Duration, model string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		data, _ := os.ReadFile(path)
		if strings.Count(string(data), s) >= want {
			return
		}
		if time.Now().After(deadline) {
			if !ollamaAnswers(t, model) {
				t.Skipf("ollama stopped completing requests mid-test; skipping rather than reporting a failure it caused")
			}
			t.Fatalf("recording never held %d occurrences of %q; have:\n%s", want, s, data)
		}
		time.Sleep(250 * time.Millisecond)
	}
}
