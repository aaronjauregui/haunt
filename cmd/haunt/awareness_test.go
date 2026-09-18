package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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

// TestWindowAwareness: the ghost is told which application has focus and
// what its window is called. Titles carry most of the signal behind "still
// on that bug?" and cost nothing to watch, which is why they come first.
func TestWindowAwareness(t *testing.T) {
	c := testenv.StartT(t)
	if _, err := exec.LookPath("foot"); err != nil {
		t.Skip("no terminal to focus")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")

	gDir := filepath.Join(dir, "watcher")
	writeGhost(t, gDir, `[ghost]
name = "watcher"
capabilities = ["window"]

[brain]
wasm = "brain.wasm"
`)
	build(t, filepath.Join(gDir, "brain.wasm"), "../../examples/whisper", "GOOS=wasip1", "GOARCH=wasm")

	record := filepath.Join(dir, "session.jsonl")
	cmd := exec.Command(bin, "run", "-x", "20", "-y", "20",
		"-state", filepath.Join(dir, "state"), "-record", record, gDir)
	cmd.Env = append(c.Env(), "HAUNT_RUNTIME_DIR="+filepath.Join(dir, "run"))
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

	if out, err := c.Swaymsg("exec", "foot"); err != nil {
		t.Skipf("cannot spawn a window: %v: %s", err, out)
	}
	waitForWindow(t, c)

	waitRecordContains(t, record, `"type":"window"`)
	waitRecordContains(t, record, `"name":"foot"`)
}

// TestWindowAwarenessNeedsGranting: without the capability the ghost is
// told nothing at all, however interesting the desktop gets.
func TestWindowAwarenessNeedsGranting(t *testing.T) {
	c := testenv.StartT(t)
	if _, err := exec.LookPath("foot"); err != nil {
		t.Skip("no terminal to focus")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")

	gDir := filepath.Join(dir, "blind")
	// No capabilities at all.
	writeGhost(t, gDir, `[ghost]
name = "blind"

[brain]
wasm = "brain.wasm"
`)
	build(t, filepath.Join(gDir, "brain.wasm"), "../../examples/whisper", "GOOS=wasip1", "GOARCH=wasm")

	record := filepath.Join(dir, "session.jsonl")
	cmd := exec.Command(bin, "run", "-x", "20", "-y", "20",
		"-state", filepath.Join(dir, "state"), "-record", record, gDir)
	cmd.Env = append(c.Env(), "HAUNT_RUNTIME_DIR="+filepath.Join(dir, "run"))
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

	if out, err := c.Swaymsg("exec", "foot"); err != nil {
		t.Skipf("cannot spawn a window: %v: %s", err, out)
	}
	waitForWindow(t, c)
	for i := 0; i < 15; i++ {
		wait100ms()
	}
	if data, _ := os.ReadFile(record); strings.Contains(string(data), `"type":"window"`) {
		t.Error("a ghost with no window capability was told about the screen")
	}
}

// seenImages records what a fake vision backend was shown.
type seenImages struct {
	mu       sync.Mutex
	requests []string
	images   [][]byte
}

func (s *seenImages) add(question string, img []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, question)
	s.images = append(s.images, img)
}

func (s *seenImages) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

// fakeVision answers a multimodal request, recording the image it was sent.
func fakeVision(t *testing.T, seen *seenImages) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct {
				Content []struct {
					Type     string `json:"type"`
					Text     string `json:"text"`
					ImageURL struct {
						URL string `json:"url"`
					} `json:"image_url"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &req); err != nil || len(req.Messages) == 0 {
			http.Error(w, "not a multimodal request", 400)
			return
		}
		var question string
		var img []byte
		for _, part := range req.Messages[0].Content {
			switch part.Type {
			case "text":
				question = part.Text
			case "image_url":
				const prefix = "data:image/png;base64,"
				if !strings.HasPrefix(part.ImageURL.URL, prefix) {
					http.Error(w, "image is not an inline png", 400)
					return
				}
				img, _ = base64.StdEncoding.DecodeString(strings.TrimPrefix(part.ImageURL.URL, prefix))
			}
		}
		seen.add(question, img)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": "They are staring at a terminal."}},
			},
		})
	}))
}

// TestLookSendsTheFocusedWindow: a look captures one window as a real PNG
// and shows it to the vision model, and the answer reaches the brain rather
// than the balloon.
func TestLookSendsTheFocusedWindow(t *testing.T) {
	c := testenv.StartT(t)
	if _, err := exec.LookPath("foot"); err != nil {
		t.Skip("no window to look at")
	}
	if _, err := exec.LookPath("grim"); err != nil {
		t.Skip("grim is how screenshots are taken")
	}

	seen := &seenImages{}
	vision := fakeVision(t, seen)
	defer vision.Close()

	dir := t.TempDir()
	rt := filepath.Join(dir, "run")
	cfgDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("[backend.vision]\nbase_url = %q\nmodel = \"fake-vision\"\n", vision.URL+"/v1")
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")
	gDir := filepath.Join(dir, "looker")
	writeGhost(t, gDir, `[ghost]
name = "looker"
capabilities = ["message", "screen"]

[brain]
wasm = "brain.wasm"
`)
	build(t, filepath.Join(gDir, "brain.wasm"), "./testdata/looker", "GOOS=wasip1", "GOARCH=wasm")

	if out, err := c.Swaymsg("exec", "foot"); err != nil {
		t.Skipf("cannot spawn a window: %v: %s", err, out)
	}
	waitForWindow(t, c)

	record := filepath.Join(dir, "session.jsonl")
	cmd := exec.Command(bin, "run", "-x", "20", "-y", "20",
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

	say := exec.Command(bin, "say", "-ghost", "looker", "look")
	say.Env = append(os.Environ(), "HAUNT_RUNTIME_DIR="+rt)
	if out, err := say.CombinedOutput(); err != nil {
		t.Fatalf("say: %v: %s", err, out)
	}

	waitRecordContains(t, record, `"type":"ai_look"`)
	waitRecordContains(t, record, "staring at a terminal")

	if seen.count() != 1 {
		t.Fatalf("the vision model was asked %d times, want 1", seen.count())
	}
	seen.mu.Lock()
	img, question := seen.images[0], seen.requests[0]
	seen.mu.Unlock()

	if len(img) < 1000 || string(img[1:4]) != "PNG" {
		t.Errorf("what was sent is not a PNG (%d bytes)", len(img))
	}
	if !strings.Contains(question, "one sentence") {
		t.Errorf("the question was %q; a vague one earns a vague answer", question)
	}

	// The answer belongs to the brain: narrating "I see a terminal" every
	// time is the tiresome version of this feature.
	if data, _ := os.ReadFile(record); strings.Count(string(data), "staring at a terminal") != 1 {
		t.Error("the vision answer appears more than once; it should not be spoken")
	}
}

// TestLookNeedsGranting: without the screen capability, looking fails and
// says why.
func TestLookNeedsGranting(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	rt := filepath.Join(dir, "run")
	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")

	gDir := filepath.Join(dir, "looker")
	writeGhost(t, gDir, `[ghost]
name = "looker"
capabilities = ["message"]

[brain]
wasm = "brain.wasm"
`)
	build(t, filepath.Join(gDir, "brain.wasm"), "./testdata/looker", "GOOS=wasip1", "GOARCH=wasm")

	record := filepath.Join(dir, "session.jsonl")
	cmd := exec.Command(bin, "run", "-x", "20", "-y", "20",
		"-state", filepath.Join(dir, "state"), "-record", record, gDir)
	cmd.Env = append(c.Env(), "HAUNT_RUNTIME_DIR="+rt)
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

	say := exec.Command(bin, "say", "-ghost", "looker", "look")
	say.Env = append(os.Environ(), "HAUNT_RUNTIME_DIR="+rt)
	if out, err := say.CombinedOutput(); err != nil {
		t.Fatalf("say: %v: %s", err, out)
	}
	waitRecordContains(t, record, "screen capability not granted")
}
