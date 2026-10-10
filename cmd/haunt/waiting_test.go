package main

import (
	"os/exec"
	"path/filepath"
	"strings"
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

// TestStallDeadline: a turn that has called a tool and then goes quiet, as
// one does while hermes waits out a provider outage with nothing on the
// stream but keepalives, is given up on after [hermes] stall_timeout. One
// of the ghost's own tools running longer than that is not, and neither is
// a turn whose model is reasoning.
func TestStallDeadline(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	rig := newHermesRig(t, dir, []fakeRule{
		{Match: "[haunt event]", Reply: "NO_REPLY"},
		{Match: "slow tool", Then: []fakeStep{{Tool: "intentions", DelayMS: 3000}}, Reply: "done at last"},
		{Match: "think hard", Tool: "intentions", StallMS: 3000, Reasoning: true, Reply: "thought it through"},
		{Match: "provider down", Tool: "intentions", StallMS: 60000, Reply: "too late"},
		{Match: "", Reply: "here at last"},
	}, "stall_timeout = 1\n")
	gDir := filepath.Join(dir, "wisp")
	writeGhost(t, gDir, `[ghost]
name = "wisp"
capabilities = ["message", "llm"]

[persona]
system = "You are Wisp."
`)
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
	waitFake(t, rig.FakeLog, 1, 30*time.Second, repliedTo("kind=boot"))

	// A tool of the ghost's that takes three times the deadline finishes.
	rig.say(t, "a slow tool")
	waitFake(t, rig.FakeLog, 1, 15*time.Second, repliedTo("slow tool"))

	// So does a model that reasons for as long after its tool call.
	rig.say(t, "think hard")
	waitFake(t, rig.FakeLog, 1, 15*time.Second, repliedTo("think hard"))

	// Keepalives alone are silence: the turn is given up on, and hermes is
	// asked to stop its run. The fake, like hermes, would not notice the
	// stream gone for seconds, and answers the stop before the run is
	// over.
	rig.say(t, "provider down")
	down := waitFakeLog(t, rig.FakeLog, 10*time.Second, turnsAbout("provider down"))
	// The person speaks again while the turn is still in flight.
	rig.say(t, "hello again")
	waitLog(t, stderr, "went quiet for 1s")
	waitFake(t, rig.FakeLog, 1, 10*time.Second, func(e fakeEntry) bool { return e.Kind == "abandoned" })

	// And the ghost hears the next thing said, once the run given up on is
	// over and not beside it.
	waitFake(t, rig.FakeLog, 1, 10*time.Second, repliedTo("hello again"))
	at := map[string]int{}
	for i, e := range fakeEntries(rig.FakeLog, func(fakeEntry) bool { return true }) {
		key := e.Kind
		switch {
		case e.Kind == "chat" && strings.Contains(e.Input, "hello again"):
			key = "hello"
		case e.Kind == "chat":
			continue
		case e.RunID != down.RunID && (e.Kind == "stop" || e.Kind == "abandoned"):
			t.Errorf("%s for run %s, want %s", e.Kind, e.RunID, down.RunID)
		}
		if _, ok := at[key]; !ok {
			at[key] = i
		}
	}
	stop, sok := at["stop"]
	over, ook := at["abandoned"]
	hello, hok := at["hello"]
	if !sok || !ook || !hok || !(stop < over && over < hello) {
		t.Errorf("stop at %d (%v), run over at %d (%v), next turn at %d (%v): want them in that order", stop, sok, over, ook, hello, hok)
	}

	if n := len(fakeEntries(rig.FakeLog, func(e fakeEntry) bool { return e.Kind == "abandoned" })); n != 1 {
		t.Errorf("%d turns abandoned, want only the one whose provider went down", n)
	}
	if strings.Contains(stderr.String(), "said nothing for") {
		t.Error("a turn hit the first-token deadline")
	}
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
