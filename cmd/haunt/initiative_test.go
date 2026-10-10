package main

import (
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/model/fake"
	"github.com/aaronjauregui/haunt/internal/testenv"
)

// TestInitiative: left alone, wisp is woken and speaks first — and only as
// often as the user's curiosity allowance permits. The idle threshold is turned right
// down so the test does not wait two minutes for the ghost to get bored,
// but kept past bootWindow: idling inside the launch grace would be counted
// a response to being started, which is exactly what that window is for.
func TestInitiative(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	// One unprompted action per day: enough to prove it happens, few enough
	// to prove it stops.
	rig := newBrainRig(t, dir, rules(
		says("kind=idle", "Still there?"),
		[]fake.Rule{silentOn("[haunt event]")},
		says("", "I hear you."),
	), "[curiosity]\nactions_per_day = 1\n")
	bin := rig.Bin
	gDir := filepath.Join(dir, "wisp")
	writeGhost(t, gDir, `[ghost]
name = "wisp"
capabilities = ["message", "llm"]

[persona]
system = "You are Wisp."
`)

	record := filepath.Join(dir, "session.jsonl")
	stateDir := filepath.Join(dir, "state")
	start := func(record string) (*exec.Cmd, *syncBuffer) {
		t.Helper()
		cmd := exec.Command(bin, "run", "-x", "100", "-y", "100", "-idle-ms", "2500",
			"-state", stateDir, "-record", record, gDir)
		cmd.Env = append(c.Env(), rig.env()...)
		// Initiative accounting is debug output: this test is about it.
		cmd.Env = append(cmd.Env, "HAUNT_DEBUG=curiosity")
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
		waitFor(t, stdout, "READY")
		return cmd, stderr
	}
	cmd, stderr := start(record)
	idles := turnsAbout("kind=idle")

	// Nobody touches anything: the compositor reports idle, the model is
	// woken, it speaks, and the driver pays for that — once.
	waitRecordContains(t, record, `"type":"idle"`)
	waitFake(t, rig.FakeLog, 1, 30*time.Second, repliedTo("kind=idle"))
	waitLog(t, stderr, "on the ghost's own account")

	// The allowance is spent. Wake the compositor and let it go idle again:
	// this time the event never reaches the model.
	ptr, err := c.NewPointer()
	if err != nil {
		t.Fatal(err)
	}
	if err := ptr.MoveTo(600, 400); err != nil {
		t.Fatal(err)
	}
	ptr.Close()
	waitRecordCount(t, record, `"type":"idle"`, 2)
	waitLog(t, stderr, "daily curiosity spent")
	if got := len(fakeEntries(rig.FakeLog, idles)); got != 1 {
		t.Errorf("the model was woken %d times, want once", got)
	}

	// A person addressing the ghost is always answered, spent or not.
	rig.say(t, "hello?")
	waitFake(t, rig.FakeLog, 1, 30*time.Second, repliedTo("hello?"))

	// And the spend survives a restart: stopping and starting the ghost is
	// not a way to refill the allowance.
	cmd.Process.Signal(syscall.SIGTERM)
	cmd.Wait()

	record2 := filepath.Join(dir, "session2.jsonl")
	cmd2, stderr2 := start(record2)
	waitRecordContains(t, record2, `"type":"idle"`)
	waitLog(t, stderr2, "daily curiosity spent")
	if got := len(fakeEntries(rig.FakeLog, idles)); got != 1 {
		t.Errorf("restart refilled the allowance: the model was woken %d times", got)
	}

	cmd2.Process.Signal(syscall.SIGTERM)
	if err := cmd2.Wait(); err != nil {
		t.Errorf("ghost exited uncleanly: %v", err)
	}
}
