package main

import (
	"encoding/json"
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

// TestBalloonMenuEndToEnd: a brain asks a question, and the person answers
// it — with the pointer, with the number keys, and by turning away.
//
// Menus are how classic ukagaka ghosts are mostly operated, and they are
// the first interaction here that works without a mouse, so both paths are
// exercised against a real compositor rather than trusted.
func TestBalloonMenuEndToEnd(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	rt := filepath.Join(dir, "run")
	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")

	gDir := filepath.Join(dir, "chooser")
	writeGhost(t, gDir, `[ghost]
name = "chooser"
capabilities = ["message"]

[brain]
wasm = "brain.wasm"
`)
	build(t, filepath.Join(gDir, "brain.wasm"), "./testdata/chooser", "GOOS=wasip1", "GOARCH=wasm")

	kb, err := c.NewKeyboard()
	if err != nil {
		t.Fatal(err)
	}
	defer kb.Close()

	const spriteX, spriteY = 200, 200
	record := filepath.Join(dir, "session.jsonl")
	cmd := exec.Command(bin, "run", "-x", "200", "-y", "200",
		"-state", filepath.Join(dir, "state"), "-record", record, gDir)
	cmd.Env = append(c.Env(), "HAUNT_RUNTIME_DIR="+rt, "HAUNT_DEBUG=balloon,wayland")
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

	// The boot menu is up: the balloon is beside the ghost and, unlike
	// every other balloon, it accepts clicks.
	waitLog(t, stderr, "choices offered")
	waitBalloon(t, c, dir, spriteX, spriteY, true)

	ptr, err := c.NewPointer()
	if err != nil {
		t.Fatal(err)
	}
	defer ptr.Close()

	// The pointer path. The menu sits to the right of the sprite; the rows
	// are stacked under the question, so aim into the panel's lower half
	// and walk down until something is picked. Which row is which is the
	// renderer's business, so the test asserts that *a* choice arrived
	// rather than pretending to know the layout.
	picked := false
	for dy := 40; dy < 140 && !picked; dy += 10 {
		if err := ptr.MoveTo(spriteX+200, spriteY+dy); err != nil {
			t.Fatal(err)
		}
		if err := ptr.Press(); err != nil {
			t.Fatal(err)
		}
		if err := ptr.Release(); err != nil {
			t.Fatal(err)
		}
		wait100ms()
		if data, _ := os.ReadFile(record); strings.Contains(string(data), `"type":"choice"`) {
			picked = true
		}
	}
	if !picked {
		t.Fatalf("clicking the menu never produced a choice:\n%s", stderr.String())
	}
	data, _ := os.ReadFile(record)
	if !strings.Contains(string(data), `"name":"tea"`) &&
		!strings.Contains(string(data), `"name":"coffee"`) &&
		!strings.Contains(string(data), `"name":"nothing"`) {
		t.Errorf("a choice event arrived with no option named:\n%s", data)
	}

	// The keyboard path: no pointer involved. The brain answers every
	// message with a fresh menu, so ask for one.
	say := exec.Command(bin, "say", "-ghost", "chooser", "again")
	say.Env = append(os.Environ(), "HAUNT_RUNTIME_DIR="+rt)
	if out, err := say.CombinedOutput(); err != nil {
		t.Fatalf("say: %v: %s", err, out)
	}
	waitLog(t, stderr, "choices offered")
	if err := kb.Key(3); err != nil { // the "2" key
		t.Fatal(err)
	}
	waitRecordContains(t, record, `"name":"coffee"`)

	// And turning away is an answer too, or a brain that asked a question
	// waits forever.
	say = exec.Command(bin, "say", "-ghost", "chooser", "once more")
	say.Env = append(os.Environ(), "HAUNT_RUNTIME_DIR="+rt)
	if out, err := say.CombinedOutput(); err != nil {
		t.Fatalf("say: %v: %s", err, out)
	}
	waitLog(t, stderr, "choices offered")
	if err := kb.Key(1); err != nil { // Escape
		t.Fatal(err)
	}
	// A dismissal is a choice event with no option named. The brain's own
	// reaction to it is an intent, which the recording does not carry —
	// the event is the contract, so the event is what this asserts on.
	waitChoiceEvent(t, record, func(e map[string]any) bool {
		_, named := e["name"]
		return !named && e["tag"] == "menu"
	})
}

// A second menu has to survive being opened. Asking for the keyboard makes
// the compositor release whoever held it, and that leave arrives after the
// new menu is up — which dismissed it within milliseconds, so from the
// outside the menu simply stopped appearing after the first time.
//
// The earlier end-to-end test missed this because it only ever opened one
// menu from a ghost that had never held the keyboard. This one opens three
// in a row, which is what a person clicking at a ghost actually does.
func TestMenusKeepComingBack(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	rt := filepath.Join(dir, "run")
	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")

	gDir := filepath.Join(dir, "chooser")
	writeGhost(t, gDir, `[ghost]
name = "chooser"
capabilities = ["message"]

[brain]
wasm = "brain.wasm"
`)
	build(t, filepath.Join(gDir, "brain.wasm"), "./testdata/chooser", "GOOS=wasip1", "GOARCH=wasm")

	// A keyboard on the seat is what makes the bug possible at all: no
	// keyboard, no focus to lose, no stale leave.
	kb, err := c.NewKeyboard()
	if err != nil {
		t.Fatal(err)
	}
	defer kb.Close()

	const spriteX, spriteY = 200, 200
	cmd := exec.Command(bin, "run", "-x", "200", "-y", "200", "-state", filepath.Join(dir, "state"), gDir)
	cmd.Env = append(c.Env(), "HAUNT_RUNTIME_DIR="+rt, "HAUNT_DEBUG=balloon")
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
	ask := func() {
		t.Helper()
		sc := exec.Command(bin, "say", "-ghost", "chooser", "menu please")
		sc.Env = env
		if out, err := sc.CombinedOutput(); err != nil {
			t.Fatalf("say: %v: %s", err, out)
		}
	}

	// Three menus in a row, each answered from the keyboard. Every one of
	// them has to still be on screen a moment after it was offered.
	for i := 1; i <= 3; i++ {
		before := strings.Count(stderr.String(), "choices offered")
		ask()
		deadline := time.Now().Add(10 * time.Second)
		for strings.Count(stderr.String(), "choices offered") == before {
			if time.Now().After(deadline) {
				t.Fatalf("menu %d was never offered:\n%s", i, stderr.String())
			}
			wait100ms()
		}

		// The moment that used to kill it: the previous holder's release
		// lands here.
		time.Sleep(700 * time.Millisecond)
		if !balloonVisible(t, c, dir, spriteX, spriteY) {
			t.Fatalf("menu %d vanished as soon as it appeared:\n%s", i, stderr.String())
		}
		// Every menu here is answered, so a dismissal at all means one was
		// taken away by something the user did not do — which is exactly
		// the bug: a stale keyboard leave waving the menu off.
		if got := strings.Count(stderr.String(), "choices dismissed"); got != 0 {
			t.Fatalf("menu %d: something dismissed a menu nobody dismissed (%d so far):\n%s",
				i, got, stderr.String())
		}

		// Answer it, which releases the keyboard and sets up the next one.
		if err := kb.Key(2); err != nil { // the "1" key
			t.Fatal(err)
		}
		waitRecordish(t, stderr, "choice taken", i)
	}
}

// waitRecordish waits for the nth occurrence of a log line.
func waitRecordish(t *testing.T, buf *syncBuffer, want string, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for strings.Count(buf.String(), want) < n {
		if time.Now().After(deadline) {
			t.Fatalf("saw %d of %q, want %d:\n%s", strings.Count(buf.String(), want), want, n, buf.String())
		}
		wait100ms()
	}
}

// The menu takes clicks, which no other balloon does — and it must not
// wander off when one of them lands slightly askew.
func TestMenuBalloonDoesNotDrag(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	rt := filepath.Join(dir, "run")
	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")

	gDir := filepath.Join(dir, "chooser")
	writeGhost(t, gDir, `[ghost]
name = "chooser"
capabilities = ["message"]

[brain]
wasm = "brain.wasm"
`)
	build(t, filepath.Join(gDir, "brain.wasm"), "./testdata/chooser", "GOOS=wasip1", "GOARCH=wasm")

	const spriteX, spriteY = 200, 200
	cmd := exec.Command(bin, "run", "-x", "200", "-y", "200", "-state", filepath.Join(dir, "state"), gDir)
	cmd.Env = append(c.Env(), "HAUNT_RUNTIME_DIR="+rt, "HAUNT_DEBUG=balloon")
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
	waitLog(t, stderr, "choices offered")

	ptr, err := c.NewPointer()
	if err != nil {
		t.Fatal(err)
	}
	defer ptr.Close()

	// A press, a wobble, a release, well inside the panel.
	before := shot(t, c, filepath.Join(dir, "before.png"))
	if err := ptr.MoveTo(spriteX+200, spriteY+30); err != nil {
		t.Fatal(err)
	}
	if err := ptr.Press(); err != nil {
		t.Fatal(err)
	}
	for _, dx := range []int{20, 40, 60} {
		if err := ptr.MoveTo(spriteX+200+dx, spriteY+30+dx); err != nil {
			t.Fatal(err)
		}
	}
	if err := ptr.Release(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)

	// The panel is where it was: a menu that slid away as you reached for
	// an option would be a cruel joke. Compared as pixels in the band the
	// panel occupies, since the surface's own position is the compositor's
	// business and not visible from here.
	after := shot(t, c, filepath.Join(dir, "after.png"))
	if !sameBand(t, before, after, spriteX+170, spriteY, 200, 100) {
		t.Error("the menu moved when it was dragged")
	}
}

// sameBand reports whether two screenshots agree over a rectangle. Used to
// ask "did this panel move" without knowing where the compositor put it.
func sameBand(t *testing.T, a, b image.Image, x, y, w, h int) bool {
	t.Helper()
	differing := 0
	for py := y; py < y+h && py < a.Bounds().Max.Y; py++ {
		for px := x; px < x+w && px < a.Bounds().Max.X; px++ {
			ar, ag, ab, _ := a.At(px, py).RGBA()
			br, bg, bb, _ := b.At(px, py).RGBA()
			if ar != br || ag != bg || ab != bb {
				differing++
			}
		}
	}
	// A few pixels of difference is the caret-free panel being redrawn at
	// the same place; a moved panel differs across most of the band.
	return differing < w*h/20
}

// waitChoiceEvent waits for a recorded choice event matching pred.
func waitChoiceEvent(t *testing.T, path string, pred func(map[string]any) bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, _ := os.ReadFile(path)
		for _, line := range strings.Split(string(data), "\n") {
			if !strings.Contains(line, `"type":"choice"`) {
				continue
			}
			var e map[string]any
			if json.Unmarshal([]byte(line), &e) != nil {
				continue
			}
			if pred(e) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no matching choice event in %s:\n%s", path, data)
		}
		wait100ms()
	}
}
