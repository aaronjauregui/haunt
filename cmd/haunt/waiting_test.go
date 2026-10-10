package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/model/fake"
	"github.com/aaronjauregui/haunt/internal/testenv"
)

// TestWaitingIndicatorAndDeadline: a model that takes its time gets a
// balloon saying so, and one that never answers is given up on rather than
// left to hold the ghost's one turn until the HTTP client gives up minutes
// later.
func TestWaitingIndicatorAndDeadline(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	// The first question is never answered; the second is, at once. That
	// pair is the whole test: the ghost must survive the first well enough
	// to serve the second.
	rig := newBrainRig(t, dir, rules(
		[]fake.Rule{silentOn("[haunt event]")},
		[]fake.Rule{{Match: "are you there", DelayMS: 60000, Calls: []fake.Call{{Name: "speak"}}}},
		says("", "here at last"),
	), "first_token_timeout = 2\n")
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

	// And a model that says nothing at all is given up on, with a reason
	// in the balloon rather than a silence.
	waitLog(t, stderr, "said nothing for 2s")

	// The turn is free again: the next thing said is answered normally,
	// which is the part that matters — one wedged turn must not deafen the
	// ghost for the rest of the session.
	rig.say(t, "hello again")
	waitFake(t, rig.FakeLog, 1, 10*time.Second, repliedTo("hello again"))
}

// TestStallDeadline: a call whose stream goes quiet after it has started,
// as one does when a provider goes down mid-answer, is given up on after
// [model] stall_timeout; the deadline is on each gap, so words or
// reasoning that keep coming slowly are not, however long they take in
// all. The person's words sent meanwhile are answered once the turn given
// up on is over.
func TestStallDeadline(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	rig := newBrainRig(t, dir, rules(
		[]fake.Rule{silentOn("[haunt event]")},
		[]fake.Rule{{Match: "slow words", AfterTool: speakCue, Reply: "one word at a time, slowly", GapMS: 600}},
		[]fake.Rule{{Match: "slow words", Calls: []fake.Call{{Name: "speak"}}}},
		[]fake.Rule{{Match: "think hard", AfterTool: speakCue, Reply: "thought it through"}},
		[]fake.Rule{{Match: "think hard", Reasoning: "hmm, let me see", GapMS: 600, Calls: []fake.Call{{Name: "speak"}}}},
		[]fake.Rule{{Match: "provider down", AfterTool: speakCue, Reply: "too late for anyone", GapMS: 3000}},
		[]fake.Rule{{Match: "provider down", Calls: []fake.Call{{Name: "speak"}}}},
		says("", "here at last"),
	// The first-token deadline is short too: the client checks for a stall
	// when the first deadline's timer fires, so a stall is caught no
	// sooner than first_token_timeout after the call began.
	), "first_token_timeout = 4\nstall_timeout = 1\n")
	gDir := filepath.Join(dir, "wisp")
	writeGhost(t, gDir, `[ghost]
name = "wisp"
capabilities = ["message", "llm"]

[persona]
system = "You are Wisp."
`)
	_, stderr := rig.start(t, c, gDir, filepath.Join(dir, "state"))
	waitFake(t, rig.FakeLog, 1, 30*time.Second, repliedTo("kind=boot"))

	// Words that come slowly, each within the deadline, finish.
	rig.say(t, "slow words")
	waitFake(t, rig.FakeLog, 1, 15*time.Second, repliedTo("slow words"))

	// So does a model that reasons slowly before it decides.
	rig.say(t, "think hard")
	waitFake(t, rig.FakeLog, 1, 15*time.Second, repliedTo("think hard"))

	// A stream that stops between words is given up on.
	rig.say(t, "provider down")
	waitFakeLog(t, rig.FakeLog, 10*time.Second, turnsAbout("provider down"))
	// The person speaks again while the turn is still in flight.
	rig.say(t, "hello again")
	waitLog(t, stderr, "went quiet for 1s")

	// And the ghost hears the next thing said, after the turn given up on.
	waitFake(t, rig.FakeLog, 1, 10*time.Second, repliedTo("hello again"))
	if strings.Contains(stderr.String(), "said nothing for") {
		t.Error("a turn hit the first-token deadline")
	}
	if n := strings.Count(stderr.String(), "went quiet for"); n != 1 {
		t.Errorf("%d turns failed, want only the one whose provider went down", n)
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
