package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// fakeAIBackend serves both chat completions (streaming echo) and image
// generations (a solid orange PNG). It records the prompts the image endpoint
// was handed so tests can check what the brain actually asked for.
func fakeAIBackend(t *testing.T, prompts *imagePrompts) *httptest.Server {
	t.Helper()
	var rewrites atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Messages[0].Role != "system" || !strings.Contains(req.Messages[0].Content, "wry") {
			http.Error(w, "persona system prompt missing", 400)
			return
		}
		last := req.Messages[len(req.Messages)-1].Content
		reply := "indeed, you said: " + last
		if request, ok := cutAfter(last, "Request: "); ok {
			// A silent prompt-rewrite request: answer in tag style. Real
			// models sample a different rewrite every time, so vary this
			// one too — a brain that re-asks instead of reusing what it
			// already rewrote would miss the generation cache.
			reply = "tags: " + request
			if n := rewrites.Add(1); n > 1 {
				reply = fmt.Sprintf("%s (take %d)", reply, n)
			}
			// Silent calls are self-contained: no history, no journal.
			if len(req.Messages) != 2 || strings.Contains(req.Messages[0].Content, "likes sunsets") {
				http.Error(w, "silent chat carried conversation context", 400)
				return
			}
		} else {
			// Silent exchanges must stay out of conversation history.
			for _, m := range req.Messages[:len(req.Messages)-1] {
				if strings.Contains(m.Content, "Request: ") {
					http.Error(w, "silent chat leaked into history", 400)
					return
				}
			}
		}
		// Journal injection is visible server-side: notes ride the system
		// prompt.
		if strings.Contains(req.Messages[0].Content, "likes sunsets") {
			reply = "[noted] " + reply
		}
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
	mux.HandleFunc("/v1/images/generations", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Prompt string `json:"prompt"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		prompts.add(req.Prompt)
		img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
		for i := range img.Pix {
			img.Pix[i] = 255
		}
		for y := 0; y < 64; y++ {
			for x := 0; x < 64; x++ {
				img.SetNRGBA(x, y, color.NRGBA{R: 255, G: 128, B: 0, A: 255})
			}
		}
		var buf bytes.Buffer
		png.Encode(&buf, img)
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"b64_json": base64.StdEncoding.EncodeToString(buf.Bytes())}},
		})
	})
	return httptest.NewServer(mux)
}

// imagePrompts collects the prompts the fake image backend received.
type imagePrompts struct {
	mu   sync.Mutex
	seen []string
}

func (p *imagePrompts) add(prompt string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = append(p.seen, prompt)
}

func (p *imagePrompts) all() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.seen...)
}

// cutAfter returns what follows the first occurrence of sep.
func cutAfter(s, sep string) (string, bool) {
	_, after, ok := strings.Cut(s, sep)
	return after, ok
}

// TestAIGhostEndToEnd: muse chats through a fake LLM (persona verified
// server-side), streams into a balloon, and paints images on request.
func TestAIGhostEndToEnd(t *testing.T) {
	c := testenv.StartT(t)
	prompts := &imagePrompts{}
	backend := fakeAIBackend(t, prompts)
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

[ai.image]
base_url = %q
model = "fake"

[ai.budget]
llm_requests_per_hour = 3
image_requests_per_hour = 2
`, backend.URL+"/v1", backend.URL+"/v1")
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")
	gDir := filepath.Join(dir, "muse")
	writeGhost(t, gDir, `[ghost]
name = "muse"
capabilities = ["message", "llm", "imagine"]

[persona]
system = "You are Muse, a wry desktop spirit."

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
	say := func(text string) {
		t.Helper()
		sc := exec.Command(bin, "say", "-ghost", "muse", text)
		sc.Env = env
		if out, err := sc.CombinedOutput(); err != nil {
			t.Fatalf("say %q: %v: %s", text, err, out)
		}
	}

	// Chat: reply flows through persona backend into the record + balloon.
	say("hello muse")
	waitRecordContains(t, record, "indeed, you said: hello muse")
	waitBalloon(t, c, dir, 100, 100, true)

	// Imagine: the request is rewritten by a silent LLM call first (the fake
	// backend answers rewrite requests in tag style), then painted. The image
	// balloon appears (orange panel right of sprite) and the cache file lands
	// on disk.
	say("draw a sunset")
	waitRecordContains(t, record, `"type":"ai_image"`)
	waitOrange(t, c, dir, 100, 100)
	cache, err := filepath.Glob(filepath.Join(dir, "state", "cache", "*.png"))
	if err != nil || len(cache) != 1 {
		t.Errorf("cache files = %v, %v", cache, err)
	}
	if got := prompts.all(); len(got) != 1 || got[0] != "tags: a sunset" {
		t.Errorf("image prompts = %q, want the rewritten [tags: a sunset]", got)
	}

	// Reroll: "draw again" replays the same prompt but skips the cache, so
	// the backend is asked a second time and a second file appears.
	say("draw again")
	waitFileCount(t, filepath.Join(dir, "state", "cache", "*.png"), 2)
	if got := prompts.all(); len(got) != 2 || got[1] != "tags: a sunset" {
		t.Errorf("image prompts after reroll = %q", got)
	}

	// Asking again for the same thing is free: the brain reuses the prompt
	// it already rewrote, so the driver serves the first image from cache —
	// no rewrite call, no generation call, no new file.
	say("draw a sunset")
	waitRecordCount(t, record, `"type":"ai_image"`, 3)
	if got := prompts.all(); len(got) != 2 {
		t.Errorf("repeat request re-generated; image prompts = %q", got)
	}
	cache, err = filepath.Glob(filepath.Join(dir, "state", "cache", "*.png"))
	if err != nil || len(cache) != 2 {
		t.Errorf("cache files after repeat = %v, %v", cache, err)
	}

	// Self-notes: a remembered fact reaches the next chat's system prompt
	// (the fake backend prefixes replies with [noted] when it sees it).
	say("remember the user likes sunsets")
	waitRecordContains(t, record, `"type":"message"`)
	say("second message")
	waitRecordContains(t, record, "[noted] indeed, you said: second message")

	// The history CLI reads the conversation back: the balloon is
	// transient, and a long reply pages away, so the words have to be
	// legible somewhere that keeps them.
	hist := exec.Command(bin, "history", "-state", filepath.Join(dir, "state"))
	hist.Env = env
	hout, err := hist.CombinedOutput()
	if err != nil {
		t.Fatalf("history: %v: %s", err, hout)
	}
	if !strings.Contains(string(hout), "hello muse") {
		t.Errorf("history is missing what was said to the ghost:\n%s", hout)
	}
	if !strings.Contains(string(hout), "indeed, you said: hello muse") {
		t.Errorf("history is missing what the ghost answered:\n%s", hout)
	}
	if !strings.Contains(string(hout), "you:") || !strings.Contains(string(hout), "ghost:") {
		t.Errorf("history does not say who spoke:\n%s", hout)
	}
	// -n trims to the most recent turns.
	trimmed := exec.Command(bin, "history", "-n", "2", "-state", filepath.Join(dir, "state"))
	trimmed.Env = env
	tout, err := trimmed.CombinedOutput()
	if err != nil {
		t.Fatalf("history -n: %v: %s", err, tout)
	}
	if lines := strings.Count(strings.TrimSpace(string(tout)), "\n") + 1; lines > 4 {
		t.Errorf("history -n 2 printed %d lines:\n%s", lines, tout)
	}

	// The journal CLI reads the notes back.
	jr := exec.Command(bin, "journal", "-state", filepath.Join(dir, "state"))
	jr.Env = env
	out, err := jr.CombinedOutput()
	if err != nil {
		t.Fatalf("journal: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "the user likes sunsets") {
		t.Errorf("journal output = %s", out)
	}
	say("third message")
	waitRecordContains(t, record, `"type":"ai_error"`)
	waitRecordContains(t, record, "limit of 3 requests/hour")

	cmd.Process.Signal(syscall.SIGTERM)
	if err := cmd.Wait(); err != nil {
		t.Errorf("ghost exited uncleanly: %v", err)
	}
}

// waitRecordCount polls until the recording holds want occurrences of s.
func waitRecordCount(t *testing.T, path, s string, want int) {
	t.Helper()
	for i := 0; i < 100; i++ {
		data, _ := os.ReadFile(path)
		if n := strings.Count(string(data), s); n >= want {
			if n > want {
				t.Fatalf("recording has %d occurrences of %q, want %d", n, s, want)
			}
			return
		}
		wait100ms()
	}
	data, _ := os.ReadFile(path)
	t.Fatalf("recording never held %d occurrences of %q; have:\n%s", want, s, data)
}

// waitFileCount polls until glob matches want files.
func waitFileCount(t *testing.T, glob string, want int) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if m, err := filepath.Glob(glob); err == nil && len(m) == want {
			return
		}
		wait100ms()
	}
	m, _ := filepath.Glob(glob)
	t.Fatalf("%s: %d files, want %d", glob, len(m), want)
}

// waitOrange polls for the generated image's orange pixels beside the sprite.
func waitOrange(t *testing.T, c *testenv.Compositor, dir string, spriteX, spriteY int) {
	t.Helper()
	path := filepath.Join(dir, "orange-poll.png")
	deadline := 100
	for i := 0; i < deadline; i++ {
		img := shot(t, c, path)
		for y := spriteY; y < spriteY+200; y++ {
			for x := spriteX + 160; x < min(spriteX+560, img.Bounds().Max.X); x++ {
				r, g, b, _ := img.At(x, y).RGBA()
				if r>>8 > 200 && g>>8 > 90 && g>>8 < 170 && b>>8 < 60 {
					return
				}
			}
		}
		wait100ms()
	}
	t.Fatal("generated orange image never appeared")
}

func wait100ms() { time.Sleep(100 * time.Millisecond) }
