package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
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
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// fakeImageBackend serves image generations: a solid orange PNG. It records
// the prompts it was handed, so a test can check what the model asked for.
func fakeImageBackend(t *testing.T, prompts *imagePrompts) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/generations" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Prompt string `json:"prompt"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		prompts.add(req.Prompt)
		img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
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
	}))
	t.Cleanup(srv.Close)
	return srv
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

// TestHermesGhostDraws: the model paints by calling draw. The picture shows
// in the balloon and lands in the cache; a fresh draw paints again; the same
// prompt again is served from the cache; and the hourly image budget is the
// driver's, told to the model in words when it runs out.
func TestHermesGhostDraws(t *testing.T) {
	c := testenv.StartT(t)
	prompts := &imagePrompts{}
	backend := fakeImageBackend(t, prompts)

	dir := t.TempDir()
	rig := newHermesRig(t, dir, []fakeRule{
		{Match: "[haunt event]", Reply: "NO_REPLY"},
		{Match: "draw it again", Tool: "draw", Args: map[string]any{"prompt": "a sunset, watercolour", "fresh": true},
			Reply: "again: {tool}"},
		{Match: "draw a sunset", Tool: "draw", Args: map[string]any{"prompt": "a sunset, watercolour"},
			Reply: "there: {tool}"},
		{Match: "", Reply: "echo: {input}"},
	}, `
[backend.image]
base_url = "`+backend.URL+`/v1"
model = "fake"

[backend.budget]
image_requests_per_hour = 2
`)
	gDir := filepath.Join(dir, "wisp")
	writeGhost(t, gDir, `[ghost]
name = "wisp"
capabilities = ["message", "llm", "imagine"]

[persona]
system = "You are Wisp, a wry desktop spirit."
`)

	state := filepath.Join(dir, "state")
	cmd := exec.Command(rig.Bin, "run", "-x", "100", "-y", "100", "-state", state, gDir)
	cmd.Env = append(c.Env(), rig.env()...)
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
	cacheGlob := filepath.Join(state, "cache", "*.png")
	draws := func(e fakeEntry) bool { return e.Kind == "tool_call" && e.Name == "draw" }

	// The picture is painted, shown beside the ghost and cached.
	rig.say(t, "draw a sunset")
	waitOrange(t, c, dir, 100, 100)
	waitFileCount(t, cacheGlob, 1)
	if got := prompts.all(); len(got) != 1 || got[0] != "a sunset, watercolour" {
		t.Errorf("image prompts = %q, want the model's own", got)
	}
	call := waitFake(t, rig.FakeLog, 1, 10*time.Second, draws)[0]
	if call.IsError || !strings.Contains(call.Text, "in your balloon") {
		t.Errorf("draw answered %+v", call)
	}

	// Fresh: the same prompt, painted again, into a second file.
	rig.say(t, "draw it again")
	waitFileCount(t, cacheGlob, 2)

	// The same thing once more is free: served from the cache, with no new
	// generation and no new file.
	rig.say(t, "draw a sunset")
	waitFake(t, rig.FakeLog, 3, 10*time.Second, draws)
	if got := prompts.all(); len(got) != 2 {
		t.Errorf("a cached picture was painted again; image prompts = %q", got)
	}

	// The budget is two an hour, and both are spent: the model is told why
	// there is no picture.
	rig.say(t, "draw it again")
	refused := waitFake(t, rig.FakeLog, 4, 10*time.Second, draws)[3]
	if !refused.IsError || !strings.Contains(refused.Text, "limit of 2 requests/hour") {
		t.Errorf("an over-budget draw answered %+v", refused)
	}
	waitFileCount(t, cacheGlob, 2)
}
