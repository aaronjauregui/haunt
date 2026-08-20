package main

import (
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// TestTwoGhostsAtOnce is the whole of direction 4 in one run: two ghosts
// resident, both on screen at once, neither on top of the other, and
// `haunt say` finding the one you last spoke to without being told.
func TestTwoGhostsAtOnce(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	rt := filepath.Join(dir, "run")

	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")
	brain := filepath.Join(dir, "brain.wasm")
	build(t, brain, "../../examples/whisper", "GOOS=wasip1", "GOARCH=wasm")

	env := append(c.Env(), "HAUNT_DATA_DIR="+data, "HAUNT_RUNTIME_DIR="+rt)
	haunt := func(args ...string) (string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	// Two installed ghosts, laid out by hand: packing and signing is
	// tested elsewhere and is not what this is about.
	for _, name := range []string{"wisp", "moth"} {
		pkg := filepath.Join(data, "ghosts", name, "pkg")
		writeGhost(t, pkg, `[ghost]
name = "`+name+`"
version = "0.1.0"
capabilities = ["message"]

[brain]
wasm = "brain.wasm"
`)
		body, err := os.ReadFile(brain)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pkg, "brain.wasm"), body, 0o644); err != nil {
			t.Fatal(err)
		}
		grants := "author_key = \"\"\ncapabilities = [\"message\"]\n"
		if err := os.WriteFile(filepath.Join(data, "ghosts", name, "grants.toml"),
			[]byte(grants), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := haunt("add", name); err != nil {
			t.Fatalf("haunt add %s: %v\n%s", name, err, out)
		}
	}

	// Both are resident, and `haunt ls` says so.
	out, err := haunt("ls")
	if err != nil {
		t.Fatalf("haunt ls: %v\n%s", err, out)
	}
	for _, name := range []string{"wisp", "moth"} {
		if !strings.Contains(out, "* "+name) {
			t.Errorf("haunt ls does not mark %s as resident:\n%s", name, out)
		}
	}

	// `haunt run` with nothing to go on runs the room. The control topic
	// is turned up because what one ghost saw the other do is otherwise
	// only visible as a reaction the scripted example brains do not have.
	cmd := exec.Command(bin, "run")
	cmd.Env = append(env, "HAUNT_DEBUG=control")
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
	})
	// One READY for the session, once everybody has landed.
	waitFor(t, stdout, "READY")

	// Both ghosts are live on their own control sockets.
	for _, name := range []string{"wisp", "moth"} {
		if _, err := os.Stat(filepath.Join(rt, name+".sock")); err != nil {
			t.Errorf("%s has no control socket: %v", name, err)
		}
	}

	// Neither opened on top of the other. Both compute the same default
	// corner of the same screen, so without arbitration they would be in
	// exactly the same place; the driver says when it moves one.
	if !strings.Contains(stderr.String(), "was taken; opening at") {
		t.Errorf("the second ghost did not step aside:\n%s", stderr.String())
	}

	// Direction 5, end to end: both ghosts greet at boot, and each one is
	// in a position to see the other do it. The line names who and what,
	// because "somebody said something" is not an observation anybody can
	// react to.
	// Both halves are waited for together: they are two ghosts on two
	// connections, so one arriving says nothing about when the other will.
	if !eventually(10*time.Second, func() bool {
		saw := stderr.String()
		return strings.Contains(saw, "observed: wisp spoke") &&
			strings.Contains(saw, "observed: moth spoke")
	}) {
		t.Errorf("the ghosts did not see each other arrive:\n%s", stderr.String())
	}

	// Nobody has been spoken to yet, so an untargeted message is genuinely
	// ambiguous and should say so rather than pick.
	if out, err := haunt("say", "hello"); err == nil {
		t.Errorf("an untargeted say should refuse while the choice is open:\n%s", out)
	} else if !strings.Contains(out, "several ghosts are running") {
		t.Errorf("say refused for the wrong reason:\n%s", out)
	}

	// Speak to one of them, and that becomes the conversation you are in.
	if out, err := haunt("say", "-ghost", "moth", "hello moth"); err != nil {
		t.Fatalf("say -ghost moth: %v\n%s", err, out)
	}
	if out, err := haunt("say", "still talking to you"); err != nil {
		t.Errorf("an untargeted say should now reach moth: %v\n%s", err, out)
	}

	// And it follows the conversation when it moves.
	if out, err := haunt("say", "-ghost", "wisp", "hello wisp"); err != nil {
		t.Fatalf("say -ghost wisp: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(rt, "wisp.last")); err != nil {
		t.Errorf("wisp was spoken to but not marked as recent: %v", err)
	}

	// Both ghosts must be able to *say* something you can read. The ghost
	// in the conventional bottom-right corner would otherwise put its
	// balloon past the right edge of the display, and the one beside it
	// would put its own on top of its neighbour.
	waitPanels(t, c, dir, 2)
}

// waitPanels waits for n separate balloon panels to be visible on screen,
// none of them running off an edge. It counts runs of balloon-coloured
// pixels along a horizontal scan rather than looking near any one sprite:
// which side of a ghost its balloon ends up on is exactly what is under
// test, so a helper that assumed a side would assume the answer.
func waitPanels(t *testing.T, c *testenv.Compositor, dir string, n int) {
	t.Helper()
	path := filepath.Join(dir, "panels.png")
	deadline := time.Now().Add(15 * time.Second)
	var best int
	for {
		img := shot(t, c, path)
		if runs := widestPanelRuns(img); len(runs) >= n {
			return
		} else if len(runs) > best {
			best = len(runs)
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d balloon panels are on screen, want %d "+
				"(a balloon off the edge or behind the other ghost looks exactly like this)", best, n)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// widestPanelRuns returns the horizontal extents of the balloon-coloured
// runs on the screen's busiest scanline.
func widestPanelRuns(img image.Image) [][2]int {
	b := img.Bounds()
	var best [][2]int
	for y := b.Min.Y; y < b.Max.Y; y += 4 {
		var runs [][2]int
		start := -1
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			panel := r>>8 < 60 && g>>8 < 60 && bl>>8 > 40 && bl>>8 < 90
			switch {
			case panel && start < 0:
				start = x
			case !panel && start >= 0:
				// Narrow runs are the ghost's own outline, not a panel.
				if x-start > 80 {
					runs = append(runs, [2]int{start, x})
				}
				start = -1
			}
		}
		if start >= 0 && b.Max.X-start > 80 {
			runs = append(runs, [2]int{start, b.Max.X})
		}
		if len(runs) > len(best) {
			best = runs
		}
	}
	return best
}
