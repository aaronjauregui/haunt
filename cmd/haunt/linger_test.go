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
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// TestSlowReplyOutlivesAnOlderLine is the bug a demo found: poke the ghost,
// ask it something, and the answer flashed and vanished. The quip's hide had
// been scheduled beside the quip, fired while the model was still thinking,
// and took the reply with it — the balloon's lifetime was being timed from
// when the question was asked rather than from when the answer arrived.
//
// The ghost here is the built-in brain, poked (a four-second line) and then
// asked something the backend answers slowly.
func TestSlowReplyOutlivesAnOlderLine(t *testing.T) {
	c := testenv.StartT(t)
	const think = 6 * time.Second
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		time.Sleep(think)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"here at last, after some thought\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer backend.Close()

	dir := t.TempDir()
	rt := filepath.Join(dir, "run")
	cfgDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf("[ai.llm]\nbase_url = %q\nmodel = \"fake\"\n\n[ai]\nfirst_token_timeout = 60\n", backend.URL+"/v1")
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

	const spriteX, spriteY = 100, 100
	cmd := exec.Command(bin, "run", "-x", "100", "-y", "100",
		"-state", filepath.Join(dir, "state"), gDir)
	cmd.Env = append(c.Env(), "HAUNT_RUNTIME_DIR="+rt, "HAUNT_CONFIG_DIR="+cfgDir, "HAUNT_DEBUG=balloon")
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
		if t.Failed() {
			t.Logf("ghost stderr:\n%s", stderr.String())
		}
	})
	waitFor(t, stdout, "READY")

	// Poke it: a short-lived line, with its own hide due in four seconds.
	ptr, err := c.NewPointer()
	if err != nil {
		t.Fatal(err)
	}
	defer ptr.Close()
	if err := ptr.MoveTo(spriteX+80, spriteY+80); err != nil {
		t.Fatal(err)
	}
	ptr.Press()
	ptr.Release()
	waitBalloon(t, c, dir, spriteX, spriteY, true)

	// Ask something while that line is still up. The answer takes longer
	// than the quip's lifetime, which is the whole point.
	say := exec.Command(bin, "say", "-ghost", "wisp", "a question")
	say.Env = append(os.Environ(), "HAUNT_RUNTIME_DIR="+rt)
	if out, err := say.CombinedOutput(); err != nil {
		t.Fatalf("say: %v: %s", err, out)
	}

	// The quip goes away on its own schedule, while the model is still
	// thinking. Under the old arrangement this is the moment that armed the
	// bomb: the hide belonged to the quip but landed on whatever came next.
	waitLog(t, stderr, "line lingered its time")

	// The reply arrives afterwards, and must be on screen and stay there.
	deadline := time.Now().Add(think + 8*time.Second)
	for !balloonVisibleAfter(t, c, dir, spriteX, spriteY) {
		if time.Now().After(deadline) {
			t.Fatalf("the reply never appeared:\n%s", stderr.String())
		}
		wait100ms()
	}
	// Still there a couple of seconds later: it has its own lifetime now,
	// counted from when it arrived.
	time.Sleep(2 * time.Second)
	if !balloonVisible(t, c, dir, spriteX, spriteY) {
		t.Errorf("the reply vanished right after arriving:\n%s", stderr.String())
	}
}

// balloonVisibleAfter is balloonVisible with a distinct file name, so the
// two polls in one test do not fight over the same screenshot.
func balloonVisibleAfter(t *testing.T, c *testenv.Compositor, dir string, x, y int) bool {
	t.Helper()
	return balloonInShot(shot(t, c, filepath.Join(dir, "reply-poll.png")), x, y)
}

// TestWedgedEmbeddingsDoNotSwallowTheReply: memory is an enhancement, and
// an embedding backend that hangs rather than failing must not cost the
// ghost its voice. It did: remembering an exchange happened before the
// reply reached the brain, so a live Ollama whose chat endpoint answered in
// milliseconds while its embeddings endpoint never answered at all left the
// ghost apparently mute — the words were on screen, but no ai_reply event
// ever arrived, and the next request queued behind the stuck one forever.
func TestWedgedEmbeddingsDoNotSwallowTheReply(t *testing.T) {
	c := testenv.StartT(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if strings.Contains(r.URL.Path, "embeddings") {
			<-r.Context().Done() // the wedge
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a reply\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer backend.Close()

	dir := t.TempDir()
	rt := filepath.Join(dir, "run")
	cfgDir := filepath.Join(dir, "config")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf("[ai.llm]\nbase_url = %q\nmodel = \"fake\"\n\n[ai.embed]\nmodel = \"fake-embed\"\n", backend.URL+"/v1")
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
	record := filepath.Join(dir, "session.jsonl")
	cmd := exec.Command(bin, "run", "-x", "100", "-y", "100",
		"-state", filepath.Join(dir, "state"), "-record", record, gDir)
	cmd.Env = append(c.Env(), "HAUNT_RUNTIME_DIR="+rt, "HAUNT_CONFIG_DIR="+cfgDir, "HAUNT_DEBUG=memory")
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
		if t.Failed() {
			t.Logf("ghost stderr:\n%s", stderr.String())
		}
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

	// The reply reaches the brain even though the memory write is stuck.
	say("first question")
	waitRecordContains(t, record, `"type":"ai_reply"`)

	// And the ghost is not deaf afterwards: the stuck embedding must not
	// hold the one AI slot the ghost has.
	say("second question")
	waitRecordCount(t, record, `"type":"ai_reply"`, 2)
}
