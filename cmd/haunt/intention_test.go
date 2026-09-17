package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// An intention is the one piece of a ghost's state whose whole purpose is
// to outlive the process, so the only test that means anything is one that
// actually stops the ghost and starts it again.

// TestAnIntentionSurvivesARestart: the ghost is asked to remember to do
// something, is killed, is started again, and hands the errand back.
func TestAnIntentionSurvivesARestart(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	// The model takes the errand on through intend, and says it when it
	// comes due, echoing the envelope so the test can see which errand came
	// back rather than only that something did.
	rig := newHermesRig(t, dir, []fakeRule{
		{Match: "kind=intention_due", Reply: "reminding you: {input}"},
		{Match: "[haunt event]", Reply: "NO_REPLY"},
		{Match: "remind me", Tool: "intend",
			Args:  map[string]any{"text": "check the build", "delay_ms": 2000},
			Reply: "I will: {tool}"},
	}, "")
	bin := rig.Bin
	gDir := filepath.Join(dir, "wisp")
	writeGhost(t, gDir, `[ghost]
name = "wisp"
capabilities = ["message", "llm"]

[persona]
system = "You are Wisp."
`)
	state := filepath.Join(dir, "state")
	rt := rig.RT

	// Start the ghost, ask it to remember something, and stop it.
	record := filepath.Join(dir, "first.jsonl")
	stop := startWisp(t, c, rig, gDir, state, record)
	rig.say(t, "remind me in 2 seconds to check the build")

	// What a person can see is the CLI, so that is what this waits on,
	// which also checks the one path they have to find out what their
	// ghost is carrying.
	waitIntentions(t, bin, state, rt, "check the build")
	stop()

	// Gone. The errand is on disk and nothing is running to fire it; wait
	// past its moment so the restart is genuinely picking up something
	// overdue.
	time.Sleep(3 * time.Second)
	if got := readIntentions(t, bin, state, rt); !strings.Contains(got, "check the build") {
		t.Fatalf("the errand did not outlive the ghost:\n%s", got)
	}

	// Started again, it hands the errand back of its own accord: the event
	// reaches the model after the settling delay, and the reply is shown.
	second := filepath.Join(dir, "second.jsonl")
	stop2 := startWisp(t, c, rig, gDir, state, second)
	defer stop2()

	waitRecordContainsFor(t, second, `"type":"intention"`, 60*time.Second)
	due := waitFake(t, rig.FakeLog, 1, 30*time.Second, repliedTo("kind=intention_due"))
	if !strings.Contains(due[0].Input, "check the build") || !strings.Contains(due[0].Input, "asked=true") {
		t.Errorf("intention_due envelope = %q", due[0].Input)
	}

	// And having said it, it stops carrying it.
	deadline := time.Now().Add(30 * time.Second)
	for {
		got := readIntentions(t, bin, state, rt)
		if !strings.Contains(got, "check the build") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("it is still carrying an errand it has handed back:\n%s", got)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// waitIntentions polls the CLI until the ghost admits to carrying want.
func waitIntentions(t *testing.T, bin, state, rt, want string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		got := readIntentions(t, bin, state, rt)
		if strings.Contains(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("it never took on %q; carrying:\n%s", want, got)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// startWisp runs the ghost on the rig's fake hermes until the returned
// function is called.
func startWisp(t *testing.T, c *testenv.Compositor, rig hermesRig, gDir, state, record string) func() {
	t.Helper()
	cmd := exec.Command(rig.Bin, "run", "-x", "100", "-y", "100",
		"-state", state, "-record", record, gDir)
	cmd.Env = append(c.Env(), rig.env()...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, stdout, "READY")
	stopped := false
	return func() {
		if stopped {
			return
		}
		stopped = true
		cmd.Process.Signal(syscall.SIGTERM)
		cmd.Wait()
	}
}

// readIntentions asks the CLI what the ghost is still meaning to do, which
// is also the only way a person can find out.
func readIntentions(t *testing.T, bin, state, rt string) string {
	t.Helper()
	cmd := exec.Command(bin, "journal", "-intentions", "-state", state)
	cmd.Env = append(os.Environ(), "HAUNT_RUNTIME_DIR="+rt)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("journal -intentions: %v: %s", err, out)
	}
	return string(out)
}
