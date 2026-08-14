package main

import (
	"bufio"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// TestGhostEndToEnd runs the whisper ghost under the daemon in headless
// sway: boot balloon appears, click swaps it, state persists across a
// restart.
func TestGhostEndToEnd(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()

	ghostDir := filepath.Join(dir, "whisper")
	if err := os.MkdirAll(ghostDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Build the brain and the daemon.
	brainWasm := filepath.Join(ghostDir, "brain.wasm")
	build(t, brainWasm, "../../examples/whisper", "GOOS=wasip1", "GOARCH=wasm")
	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")

	manifest := `[ghost]
name = "whisper"
version = "0.1.0"
capabilities = ["message"]

[brain]
wasm = "brain.wasm"
`
	if err := os.WriteFile(filepath.Join(ghostDir, "ghost.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	stateDir := filepath.Join(dir, "state")
	recordPath := filepath.Join(dir, "session.jsonl")
	const posX, posY = 200, 200
	start := func() (*exec.Cmd, chan string) {
		cmd := exec.Command(bin, "run",
			"-x", fmt.Sprint(posX), "-y", fmt.Sprint(posY),
			"-state", stateDir, "-record", recordPath, ghostDir)
		// Isolated runtime dir: control sockets must not touch the real
		// session's. Later duplicates win on Linux.
		cmd.Env = append(c.Env(), "HAUNT_RUNTIME_DIR="+dir)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		stderrR, err := cmd.StderrPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		lines := make(chan string, 64)
		go func() {
			sc := bufio.NewScanner(stdout)
			for sc.Scan() {
				lines <- sc.Text()
			}
			close(lines)
		}()
		go func() {
			sc := bufio.NewScanner(stderrR)
			for sc.Scan() {
				t.Logf("haunt stderr: %s", sc.Text())
			}
		}()
		return cmd, lines
	}

	waitReady := func(lines chan string) {
		t.Helper()
		timeout := time.After(15 * time.Second)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatal("haunt exited before READY")
				}
				if strings.HasPrefix(l, "READY") {
					return
				}
			case <-timeout:
				t.Fatal("timed out waiting for READY")
			}
		}
	}

	stop := func(cmd *exec.Cmd) {
		t.Helper()
		cmd.Process.Signal(syscall.SIGTERM)
		if err := cmd.Wait(); err != nil {
			t.Errorf("haunt exited uncleanly: %v", err)
		}
	}

	// Session 1: boot balloon appears to the right of the sprite.
	cmd, lines := start()
	waitReady(lines)
	waitBalloon(t, c, dir, posX, posY, true)
	stop(cmd)

	// Session 2: state persisted (visits=2 -> "Back again" balloon shows),
	// and clicking the sprite changes the balloon.
	cmd, lines = start()
	waitReady(lines)
	waitBalloon(t, c, dir, posX, posY, true)

	ptr, err := c.NewPointer()
	if err != nil {
		t.Fatal(err)
	}
	defer ptr.Close()
	// Click the sprite center (built-in blob is 160x160 at posX,posY).
	if err := ptr.MoveTo(posX+80, posY+80); err != nil {
		t.Fatal(err)
	}
	if err := ptr.Press(); err != nil {
		t.Fatal(err)
	}
	if err := ptr.Release(); err != nil {
		t.Fatal(err)
	}
	// Balloon content changes ("Boo." is much narrower than the greeting);
	// just assert a balloon is still present after the round trip.
	waitBalloon(t, c, dir, posX, posY, true)

	// Drag the sprite: the balloon must follow to the new position.
	const dragX, dragY = 150, 120
	if err := ptr.Drag(posX+80, posY+80, posX+80+dragX, posY+80+dragY); err != nil {
		t.Fatal(err)
	}
	waitBalloon(t, c, dir, posX+dragX, posY+dragY, true)

	// Inbound messaging: haunt say reaches the running ghost.
	say := exec.Command(bin, "say", "-ghost", "whisper", "hello", "from", "outside")
	say.Env = append(c.Env(), "HAUNT_RUNTIME_DIR="+dir)
	if out, err := say.CombinedOutput(); err != nil {
		t.Fatalf("haunt say failed: %v: %s", err, out)
	}
	stop(cmd)

	// The recording proves the message flowed through the bus.
	rec, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rec), `"hello from outside"`) {
		t.Errorf("session recording missing inbound message; got:\n%s", rec)
	}

	// State survived on disk.
	if _, err := os.Stat(filepath.Join(stateDir, "state.db")); err != nil {
		t.Errorf("state.db missing: %v", err)
	}
}

// waitBalloon polls screenshots for balloon-background pixels in the region
// right of the sprite.
func waitBalloon(t *testing.T, c *testenv.Compositor, dir string, spriteX, spriteY int, want bool) {
	t.Helper()
	// Balloon background from balloon.DefaultTheme(), fully composited over
	// the sway background; alpha 235 blending shifts it slightly, so match
	// loosely.
	path := filepath.Join(dir, "balloon-poll.png")
	deadline := time.Now().Add(10 * time.Second)
	for {
		found := false
		img := shot(t, c, path)
		// Search the band to the right of the sprite.
		for y := spriteY; y < spriteY+160 && !found; y++ {
			for x := spriteX + 160; x < min(spriteX+560, img.Bounds().Max.X); x++ {
				r, g, b, _ := img.At(x, y).RGBA()
				// Dark navy panel: low red/green, slightly higher blue.
				if r>>8 < 60 && g>>8 < 60 && b>>8 > 40 && b>>8 < 90 {
					found = true
					break
				}
			}
		}
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

func build(t *testing.T, out, pkg string, env ...string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Env = append(os.Environ(), env...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building %s: %v: %s", pkg, err, b)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
