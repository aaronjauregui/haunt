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

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// TestWhyDidYouSayThat: a reply keeps its reasons, and `haunt history -why`
// hands them back. The point of the feature is trust, so the test is that a
// person can see the memory that came back and the prompt it went into —
// not that some bytes were written.
func TestWhyDidYouSayThat(t *testing.T) {
	c := testenv.StartT(t)
	backend := whyBackend(t)
	defer backend.Close()

	dir := t.TempDir()
	rt := filepath.Join(dir, "run")
	cfgDir := filepath.Join(dir, "config")
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`[ai.llm]
base_url = %q
model = "fake"

[ai.embed]
base_url = %q
model = "fake-embed"
`, backend.URL+"/v1", backend.URL+"/v1")
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
system = "You are Wisp, a wry desktop spirit."
`)

	cmd := exec.Command(bin, "run", "-x", "100", "-y", "100", "-state", stateDir, gDir)
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
	say := func(text string) {
		t.Helper()
		sc := exec.Command(bin, "say", "-ghost", "wisp", text)
		sc.Env = env
		if out, err := sc.CombinedOutput(); err != nil {
			t.Fatalf("say %q: %v: %s", text, err, out)
		}
	}

	// Two exchanges: the first becomes a memory, the second is the one
	// whose reasons we ask about.
	say("I like lapsang souchong")
	waitBalloon(t, c, dir, 100, 100, true)
	say("what tea should I get")
	waitBalloon(t, c, dir, 100, 100, true)

	// Stop the ghost so the store is closed and readable.
	cmd.Process.Signal(syscall.SIGTERM)
	cmd.Wait()

	out := hauntOut(t, bin, env, "history", "-state", stateDir, "-why")
	// The reply is there, and so is an account of it.
	if !strings.Contains(out, "what tea should I get") {
		t.Fatalf("history did not show the exchange:\n%s", out)
	}
	if !strings.Contains(out, "recalled") {
		t.Errorf("history -why said nothing about recall:\n%s", out)
	}
	if !strings.Contains(out, "turns of this conversation") {
		t.Errorf("history -why did not say what conversation context was used:\n%s", out)
	}
	// Without -prompt the prompt stays out of the way.
	if strings.Contains(out, "the prompt it was given") {
		t.Errorf("history -why printed the prompt without being asked:\n%s", out)
	}

	// With -prompt, the persona that was actually sent is visible. That is
	// the part that makes the answer checkable rather than a claim.
	full := hauntOut(t, bin, env, "history", "-state", stateDir, "-why", "-prompt")
	if !strings.Contains(full, "the prompt it was given") {
		t.Fatalf("history -why -prompt did not print the prompt:\n%s", full)
	}
	if !strings.Contains(full, "wry desktop spirit") {
		t.Errorf("the prompt shown is not the persona that was sent:\n%s", full)
	}

	// Plain history is unchanged: the reasons are opt-in.
	plain := hauntOut(t, bin, env, "history", "-state", stateDir)
	if strings.Contains(plain, "recalled") || strings.Contains(plain, "·") {
		t.Errorf("plain history grew an explanation it was not asked for:\n%s", plain)
	}
}

func hauntOut(t *testing.T, bin string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("haunt %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// whyBackend answers chat and embeddings. Embeddings are derived from the
// text so related words land near each other, which is what makes the
// recall in this test a real selection rather than a coin toss.
func whyBackend(t *testing.T) *httptest.Server {
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
		last := req.Messages[len(req.Messages)-1].Content
		reply := "you asked about: " + last
		// Streamed, like the real thing: the driver reads SSE deltas.
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for _, word := range strings.Fields(reply) {
			b, _ := json.Marshal(map[string]any{"choices": []map[string]any{
				{"delta": map[string]string{"content": word + " "}},
			}})
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input any `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		texts := embedInputs(req.Input)
		data := make([]map[string]any, 0, len(texts))
		for i, s := range texts {
			data = append(data, map[string]any{"index": i, "embedding": bagOfWords(s)})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	return httptest.NewServer(mux)
}

func embedInputs(in any) []string {
	switch v := in.(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return []string{""}
}

// bagOfWords is a crude but honest embedding: one dimension per letter, so
// texts sharing words score high against each other and unrelated ones do
// not. Enough for recall to have a real distribution to select from.
func bagOfWords(s string) []float64 {
	v := make([]float64, 26)
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' {
			v[r-'a']++
		}
	}
	return v
}
