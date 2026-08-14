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
	"sync"
	"syscall"
	"testing"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// memoryVocab is the fake embedder's whole world: a vector counts these
// words, so texts that share them score similar and texts that do not score
// zero. Enough to exercise retrieval without a model.
var memoryVocab = []string{"tea", "oolong", "morning", "weather", "socks", "compiler"}

// systemPrompts records the system prompt of every chat request.
type systemPrompts struct {
	mu   sync.Mutex
	seen []string
}

func (p *systemPrompts) add(s string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = append(p.seen, s)
}

func (p *systemPrompts) last() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.seen) == 0 {
		return ""
	}
	return p.seen[len(p.seen)-1]
}

func (p *systemPrompts) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.seen)
}

// fakeMemoryBackend serves chat and embeddings: replies are canned, vectors
// are bag-of-vocab counts.
func fakeMemoryBackend(t *testing.T, prompts *systemPrompts) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if len(req.Messages) > 0 && req.Messages[0].Role == "system" {
			prompts.add(req.Messages[0].Content)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"noted\"}}]}\n\ndata: [DONE]\n\n")
	})
	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		vec := make([]float32, len(memoryVocab))
		for i, word := range memoryVocab {
			vec[i] = float32(strings.Count(strings.ToLower(req.Input), word))
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": vec}},
		})
	})
	return httptest.NewServer(mux)
}

// TestLongTermMemory: a fact mentioned early falls out of the history
// window, and comes back into the system prompt when something reminds the
// ghost of it — while unrelated chatter does not drag it along.
func TestLongTermMemory(t *testing.T) {
	c := testenv.StartT(t)
	prompts := &systemPrompts{}
	backend := fakeMemoryBackend(t, prompts)
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

[ai.embed]
model = "fake-embed"

[ai.budget]
llm_requests_per_hour = 20
`, backend.URL+"/v1")
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")
	gDir := filepath.Join(dir, "muse")
	// history_turns = 1: only the previous exchange rides along, so
	// anything older can reach the prompt through recall or not at all.
	writeGhost(t, gDir, `[ghost]
name = "muse"
capabilities = ["message", "llm"]

[persona]
system = "You are Muse."
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
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGTERM)
		cmd.Wait()
	})
	waitFor(t, stdout, "READY")

	env := append(os.Environ(), "HAUNT_RUNTIME_DIR="+rt)
	rawSay := func(text string) {
		t.Helper()
		sc := exec.Command(bin, "say", "-ghost", "muse", text)
		sc.Env = env
		if out, err := sc.CombinedOutput(); err != nil {
			t.Fatalf("say %q: %v: %s", text, err, out)
		}
	}
	// say waits for the reply, which lands after the exchange is embedded,
	// so the next message sees a settled index.
	replies := 0
	say := func(text string) {
		t.Helper()
		rawSay(text)
		replies++
		waitRecordCount(t, record, `"type":"ai_reply"`, replies)
	}

	say("I drink oolong tea every morning")
	say("my socks are wet")
	say("the compiler is angry again")

	// Unrelated chatter recalls nothing: the tea exchange shares no words
	// with it, so it stays out of the prompt.
	if got := prompts.last(); strings.Contains(got, "oolong") {
		t.Errorf("unrelated message recalled the tea exchange:\n%s", got)
	}

	// Now something that rhymes with it semantically. The exchange is long
	// past the one-turn history window, so recall is the only path in.
	say("what tea should I buy")
	got := prompts.last()
	if !strings.Contains(got, "Things this reminds you of") {
		t.Errorf("no recall section in the system prompt:\n%s", got)
	}
	if !strings.Contains(got, "oolong") {
		t.Errorf("recall missed the tea exchange:\n%s", got)
	}
	if prompts.count() != replies {
		t.Errorf("chat requests = %d, want %d", prompts.count(), replies)
	}

	// Notes join the index too, and recall dedupes against the journal
	// block so a recent note is not pasted in twice.
	rawSay("remember the user prefers oolong over black tea") // a note, not a chat
	say("tea again")
	got = prompts.last()
	if strings.Count(got, "prefers oolong over black tea") != 1 {
		t.Errorf("note should appear exactly once (journal or recall, not both):\n%s", got)
	}

	cmd.Process.Signal(syscall.SIGTERM)
	if err := cmd.Wait(); err != nil {
		t.Errorf("ghost exited uncleanly: %v", err)
	}
}
