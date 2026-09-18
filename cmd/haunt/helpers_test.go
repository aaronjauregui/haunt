package main

import (
	"bufio"
	"bytes"
	"fmt"
	"image"
	"image/png"
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
	for i := 0; i < 100; i++ {
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

// waitFileContains polls a file a tool server writes to, which is how a
// test tells "the ghost did something" from "the ghost said it had".
func waitFileContains(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never contained %q; have:\n%s", path, want, data)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// waitFake waits until the fake hermes has logged want entries matching.
func waitFake(t *testing.T, path string, want int, d time.Duration, match func(fakeEntry) bool) []fakeEntry {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		got := fakeEntries(path, match)
		if len(got) >= want {
			return got
		}
		if time.Now().After(deadline) {
			data, _ := os.ReadFile(path)
			t.Fatalf("fakehermes logged %d matching entries, want %d; its log:\n%s", len(got), want, data)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// startGhost launches `haunt run` for a ghost dir with an isolated runtime
// dir, waiting for READY.
func startGhost(t *testing.T, c *testenv.Compositor, bin, ghostDir, stateDir, recordPath, runtimeDir string, x, y int) *exec.Cmd {
	t.Helper()
	args := []string{"run", "-x", fmt.Sprint(x), "-y", fmt.Sprint(y), "-state", stateDir}
	if recordPath != "" {
		args = append(args, "-record", recordPath)
	}
	args = append(args, ghostDir)
	cmd := exec.Command(bin, args...)
	cmd.Env = append(c.Env(), "HAUNT_RUNTIME_DIR="+runtimeDir)
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
	return cmd
}

func stopGhost(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	cmd.Process.Signal(syscall.SIGTERM)
	if err := cmd.Wait(); err != nil {
		t.Errorf("ghost exited uncleanly: %v", err)
	}
}

func writeGhost(t *testing.T, dir, manifest string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ghost.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

// waitRecordContains polls a session recording for a substring.
func waitRecordContains(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("recording %s never contained %q; have:\n%s", path, want, data)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func waitFor(t *testing.T, r interface{ Read([]byte) (int, error) }, prefix string) {
	t.Helper()
	found := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), prefix) {
				found <- true
				return
			}
		}
		found <- false
	}()
	select {
	case ok := <-found:
		if !ok {
			t.Fatalf("stream ended before %q", prefix)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("timed out waiting for %q", prefix)
	}
}

// waitLog polls the daemon's stderr for a line.
func waitLog(t *testing.T, buf *syncBuffer, want string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if strings.Contains(buf.String(), want) {
			return
		}
		wait100ms()
	}
	t.Fatalf("log never contained %q; have:\n%s", want, buf.String())
}

// balloonVisible reports whether a balloon panel is on screen right now.
func balloonVisible(t *testing.T, c *testenv.Compositor, dir string, spriteX, spriteY int) bool {
	t.Helper()
	return balloonInShot(shot(t, c, filepath.Join(dir, "balloon-now.png")), spriteX, spriteY)
}

// balloonInShot looks for the panel's dark navy in the band beside the
// sprite.
func balloonInShot(img image.Image, spriteX, spriteY int) bool {
	for y := spriteY; y < spriteY+160; y++ {
		for x := spriteX + 160; x < min(spriteX+560, img.Bounds().Max.X); x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if r>>8 < 60 && g>>8 < 60 && b>>8 > 40 && b>>8 < 90 {
				return true
			}
		}
	}
	return false
}

// waitBalloon polls screenshots until a balloon is, or is not, beside the
// sprite.
func waitBalloon(t *testing.T, c *testenv.Compositor, dir string, spriteX, spriteY int, want bool) {
	t.Helper()
	// Balloon background from balloon.DefaultTheme(), fully composited over
	// the sway background; alpha 235 blending shifts it slightly, so match
	// loosely.
	path := filepath.Join(dir, "balloon-poll.png")
	deadline := time.Now().Add(10 * time.Second)
	for {
		found := balloonInShot(shot(t, c, path), spriteX, spriteY)
		if found == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("balloon presence = %v, want %v", found, want)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func shot(t *testing.T, c *testenv.Compositor, path string) image.Image {
	t.Helper()
	if err := c.Screenshot(path); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// TestMain gives the package one directory for built fixtures, so the
// daemon, the whisper brain and the fakes are compiled once per run rather
// than once per test that wants them.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "haunt-test-build-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	builds.dir = dir
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// builds is the cache of compiled fixtures, keyed by package and build
// environment. Tests run in parallel, so a build is held under its own
// entry while it happens and the others wait for it rather than starting
// a second one.
var builds struct {
	mu   sync.Mutex
	dir  string
	done map[string]*buildResult
}

type buildResult struct {
	ready chan struct{}
	path  string
	err   error
}

// build compiles pkg with the given environment into out, from the cache
// when it has been built before in this run.
func build(t *testing.T, out, pkg string, env ...string) {
	t.Helper()
	key := pkg + "\x00" + strings.Join(env, "\x00")
	builds.mu.Lock()
	if builds.done == nil {
		builds.done = map[string]*buildResult{}
	}
	r, ok := builds.done[key]
	if !ok {
		r = &buildResult{ready: make(chan struct{}), path: filepath.Join(builds.dir, fmt.Sprint(len(builds.done)))}
		builds.done[key] = r
		builds.mu.Unlock()
		cmd := exec.Command("go", "build", "-o", r.path, pkg)
		cmd.Env = append(os.Environ(), env...)
		if b, err := cmd.CombinedOutput(); err != nil {
			r.err = fmt.Errorf("building %s: %v: %s", pkg, err, b)
		}
		close(r.ready)
	} else {
		builds.mu.Unlock()
		<-r.ready
	}
	if r.err != nil {
		t.Fatal(r.err)
	}
	data, err := os.ReadFile(r.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, data, 0o755); err != nil {
		t.Fatal(err)
	}
}
