package main

import (
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// TestWaitingIndicatorAndDeadline: a model that takes its time gets a
// balloon saying so, and a gateway that never answers is abandoned rather
// than left to hold the ghost's one turn until the HTTP client gives up
// minutes later.
func TestWaitingIndicatorAndDeadline(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	// The first question is never answered; the second is, at once. That
	// pair is the whole test: the ghost must survive the first well enough
	// to serve the second.
	rig := newHermesRig(t, dir, []fakeRule{
		{Match: "[haunt event]", Reply: "NO_REPLY"},
		{Match: "are you there", DelayMS: 60000, Reply: "too late"},
		{Match: "", Reply: "here at last"},
	}, "first_token_timeout = 2\n")
	gDir := filepath.Join(dir, "wisp")
	writeGhost(t, gDir, `[ghost]
name = "wisp"
capabilities = ["message", "llm"]

[persona]
system = "You are Wisp."
`)

	const spriteX, spriteY = 100, 100
	cmd := exec.Command(rig.Bin, "run", "-x", "100", "-y", "100",
		"-state", filepath.Join(dir, "state"), gDir)
	cmd.Env = append(c.Env(), rig.env()...)
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

	// The boot turn has to be out of the way first, or what we see next is
	// something other than the indicator.
	waitFake(t, rig.FakeLog, 1, 30*time.Second, repliedTo("kind=boot"))
	waitNoBalloon(t, c, dir, spriteX, spriteY)

	// A wait the person would notice is shown as one.
	rig.say(t, "are you there?")
	waitBalloon(t, c, dir, spriteX, spriteY, true)

	// And a gateway that says nothing at all is given up on, with a reason
	// in the balloon rather than a silence.
	waitLog(t, stderr, "said nothing for 2s")
	waitFake(t, rig.FakeLog, 1, 10*time.Second, func(e fakeEntry) bool { return e.Kind == "abandoned" })

	// The turn is free again: the next thing said is answered normally,
	// which is the part that matters — one wedged turn must not deafen the
	// ghost for the rest of the session.
	rig.say(t, "hello again")
	waitFake(t, rig.FakeLog, 1, 10*time.Second, repliedTo("hello again"))
}

// waitNoBalloon waits until nothing is beside the sprite.
func waitNoBalloon(t *testing.T, c *testenv.Compositor, dir string, x, y int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if !balloonVisible(t, c, dir, x, y) {
			return
		}
		wait100ms()
	}
	t.Fatal("a balloon never went away")
}
