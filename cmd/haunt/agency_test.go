package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	"github.com/aaronjauregui/haunt/internal/model/fake"
	"github.com/aaronjauregui/haunt/internal/skill"
	"github.com/aaronjauregui/haunt/internal/testenv"
)

// Agency, end to end: a real compositor, a real driver, a real MCP server
// on the end of a real pipe, and a file on disk that only exists if the
// ghost actually did something.
//
// Both paths are covered, because they are different decisions: the model
// deciding on a tool in the middle of a turn, and a brain asking for one
// specific thing at one specific moment.

// actingRun is a ghost with one tool server, and the paths the test needs.
// startActingGhost's brain is a testdata program, or empty for the model.
type actingRun struct {
	dir     string
	bin     string
	rt      string
	record  string
	notes   string
	fakeLog string
}

func startActingGhost(t *testing.T, c *testenv.Compositor, brain string) actingRun {
	t.Helper()
	dir := t.TempDir()
	// The model decides on the server's tool by its wire name.
	rig := newBrainRig(t, dir, rules(
		[]fake.Rule{silentOn("[haunt event]")},
		acts("note that", []fake.Call{decision("notes__append", map[string]any{"text": "milk"})},
			"there, it is written down"),
	), "")
	bin := rig.Bin
	server := filepath.Join(dir, "toolserver")
	build(t, server, "./testdata/toolserver")

	notes := filepath.Join(dir, "notes.txt")
	gDir := filepath.Join(dir, "wisp")
	brainSection := ""
	if brain != "" {
		brainSection = "[brain]\ncommand = [\"./brain\"]\n"
	}
	manifest := fmt.Sprintf(`[ghost]
name = "wisp"
capabilities = ["message", "llm", "tools"%s]

%s
[persona]
system = "You are Wisp."

[[tool]]
name = "notes"
command = [%q]
env = ["HAUNT_TEST_NOTES"]
`, brainCap(brain), brainSection, server)
	writeGhost(t, gDir, manifest)
	if brain != "" {
		build(t, filepath.Join(gDir, "brain"), "./testdata/"+brain)
	}

	record := filepath.Join(dir, "session.jsonl")
	cmd := exec.Command(bin, "run", "-x", "100", "-y", "100",
		"-state", filepath.Join(dir, "state"), "-record", record, gDir)
	cmd.Env = append(c.Env(), rig.env()...)
	cmd.Env = append(cmd.Env,
		"HAUNT_TEST_NOTES="+notes,
		// A variable the manifest does not name, to check it stays behind.
		"HAUNT_TEST_SECRET=do-not-pass-this-on",
	)
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

	return actingRun{dir: dir, bin: bin, rt: rig.RT, record: record, notes: notes, fakeLog: rig.FakeLog}
}

func brainCap(brain string) string {
	if brain == "" {
		return ""
	}
	return `, "native-brain"`
}

func (r actingRun) say(t *testing.T, text string) {
	t.Helper()
	sc := exec.Command(r.bin, "say", "-ghost", "wisp", text)
	sc.Env = append(os.Environ(), "HAUNT_RUNTIME_DIR="+r.rt)
	if out, err := sc.CombinedOutput(); err != nil {
		t.Fatalf("say %q: %v: %s", text, err, out)
	}
}

// TestGhostActsThroughTheModel: art, a persona, a tool server and no code
// anywhere. The model is offered the tool as a decision, the driver runs
// it, and a file on disk changes — which is the difference
// between a companion that can do things and one that can describe doing
// them.
func TestGhostActsThroughTheModel(t *testing.T) {
	c := testenv.StartT(t)
	run := startActingGhost(t, c, "")

	run.say(t, "note that I need milk")

	// The thing itself: the tool ran and wrote to the file.
	waitFileContains(t, run.notes, "milk")
	// And then the model answered, with the tool's result in front of it.
	waitFakeLog(t, run.fakeLog, 10*time.Second, repliedTo("note that"))

	list := waitFakeLog(t, run.fakeLog, time.Second, turnsAbout("note that"))
	if !slices.Contains(list.Tools, "notes__append") {
		t.Errorf("the tools never reached the model: %v", list.Tools)
	}
	// A package skill that needs the server's tool names it as the model
	// is offered it, and the tools the model was offered satisfy it.
	pkg := fstest.MapFS{"shopping/SKILL.md": {Data: []byte("---\nname: shopping\ndescription: x\n" +
		"metadata:\n  haunt:\n    requires_tools: [notes__append]\n---\n")}}
	if h, err := skill.Open(pkg, "", list.Tools); err != nil || len(h.List()) != 1 {
		t.Errorf("a skill needing notes__append is hidden (%v)", err)
	}
	call := waitFakeLog(t, run.fakeLog, time.Second, decided("notes__append"))
	if call.IsError || !strings.Contains(call.Text, "appended milk") {
		t.Errorf("tool result = %+v, want what the server said", call)
	}
}

// Acting is visibly different from talking, and the journal is the mark
// that survives the moment: it is there whether or not anybody was watching
// the balloon while the tool ran.
func TestActingIsInTheJournal(t *testing.T) {
	c := testenv.StartT(t)
	run := startActingGhost(t, c, "")

	run.say(t, "note that I need milk")
	waitFileContains(t, run.notes, "milk")

	deadline := time.Now().Add(10 * time.Second)
	for {
		out, err := run.journal(t)
		if err == nil && strings.Contains(out, "notes.append") {
			if !strings.Contains(out, "milk") {
				t.Errorf("journal names the tool but not what it was given:\n%s", out)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the journal never mentioned the action:\n%s (%v)", out, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (r actingRun) journal(t *testing.T) (string, error) {
	t.Helper()
	cmd := exec.Command(r.bin, "journal", "-state", filepath.Join(r.dir, "state"), "wisp")
	cmd.Env = append(os.Environ(), "HAUNT_RUNTIME_DIR="+r.rt)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestBrainActsDirectly: the other path, where a brain asks for one
// particular thing rather than leaving it to the model. The result comes
// back as an `acted` event, which is what a brain waits on.
func TestBrainActsDirectly(t *testing.T) {
	c := testenv.StartT(t)
	run := startActingGhost(t, c, "actor")

	run.say(t, "remember the milk")

	waitFileContains(t, run.notes, "remember the milk")
	waitRecordContains(t, run.record, `"type":"acted"`)
	waitRecordContains(t, run.record, "appended remember the milk")
}

// The environment a tool server runs in is what the manifest named and
// nothing else: a package may ask for a credential and can never carry one,
// and it does not get handed the rest of the user's environment either.
func TestToolServerEnvironmentIsFiltered(t *testing.T) {
	c := testenv.StartT(t)
	run := startActingGhost(t, c, "")

	run.say(t, "note that I need milk")
	waitFileContains(t, run.notes, "milk")

	// The named variable reached the server: it wrote to the file that
	// variable points at, which is the only way it could have known where.
	// The unnamed one is reported by the server itself, since the absence
	// of something is not visible from out here.
	data, err := os.ReadFile(run.notes)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "leaked") {
		t.Errorf("a variable the manifest never named reached the tool server:\n%s", data)
	}
}
