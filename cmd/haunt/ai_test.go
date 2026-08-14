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
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// fakeAIBackend serves both chat completions (streaming echo) and image
// generations (a solid orange PNG).
func fakeAIBackend(t *testing.T) *httptest.Server {
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
		if req.Messages[0].Role != "system" || !strings.Contains(req.Messages[0].Content, "wry") {
			http.Error(w, "persona system prompt missing", 400)
			return
		}
		last := req.Messages[len(req.Messages)-1].Content
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for _, word := range strings.Fields("indeed, you said: " + last) {
			b, _ := json.Marshal(map[string]any{"choices": []map[string]any{
				{"delta": map[string]string{"content": word + " "}},
			}})
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	mux.HandleFunc("/v1/images/generations", func(w http.ResponseWriter, r *http.Request) {
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

// TestAIGhostEndToEnd: muse chats through a fake LLM (persona verified
// server-side), streams into a balloon, and paints images on request.
func TestAIGhostEndToEnd(t *testing.T) {
	c := testenv.StartT(t)
	backend := fakeAIBackend(t)
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
llm_requests_per_hour = 2
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

	// Imagine: image balloon appears (orange panel right of sprite) and the
	// cache file lands on disk.
	say("draw a sunset")
	waitRecordContains(t, record, `"type":"ai_image"`)
	waitOrange(t, c, dir, 100, 100)
	cache, err := filepath.Glob(filepath.Join(dir, "state", "cache", "*.png"))
	if err != nil || len(cache) != 1 {
		t.Errorf("cache files = %v, %v", cache, err)
	}

	// Budget: llm limit is 2; this second chat uses it up, the third fails.
	say("second message")
	waitRecordContains(t, record, "indeed, you said: second message")
	say("third message")
	waitRecordContains(t, record, `"type":"ai_error"`)
	waitRecordContains(t, record, "limit of 2 requests/hour")

	cmd.Process.Signal(syscall.SIGTERM)
	if err := cmd.Wait(); err != nil {
		t.Errorf("ghost exited uncleanly: %v", err)
	}
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
