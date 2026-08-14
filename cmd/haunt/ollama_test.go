package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

const ollamaURL = "http://localhost:11434"

// ollamaModel returns a usable local model name, or "" when Ollama is not
// running or has no models. HAUNT_TEST_OLLAMA_MODEL overrides the pick.
func ollamaModel(t *testing.T) string {
	t.Helper()
	if m := os.Getenv("HAUNT_TEST_OLLAMA_MODEL"); m != "" {
		return m
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(ollamaURL + "/v1/models")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil || len(out.Data) == 0 {
		return ""
	}
	return out.Data[0].ID
}

// TestOllamaGhost runs muse against a real local Ollama server: a live LLM
// reply must stream back and reach the session record. Skips when Ollama is
// unavailable, so CI without it stays green.
func TestOllamaGhost(t *testing.T) {
	model := ollamaModel(t)
	if model == "" {
		t.Skip("no local Ollama server with a model; skipping live LLM test")
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
	waitRecordContainsSlow(t, record, `"type":"ai_reply"`, 120*time.Second)
	waitBalloon(t, c, dir, 100, 100, true)

	cmd.Process.Signal(syscall.SIGTERM)
	if err := cmd.Wait(); err != nil {
		t.Errorf("ghost exited uncleanly: %v", err)
	}
}

// waitRecordContainsSlow is waitRecordContains with a custom deadline.
func waitRecordContainsSlow(t *testing.T, path, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("recording never contained %q; have:\n%s", want, data)
		}
		time.Sleep(250 * time.Millisecond)
	}
}
