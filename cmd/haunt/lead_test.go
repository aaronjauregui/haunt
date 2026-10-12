package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/model/fake"
	"github.com/aaronjauregui/haunt/internal/testenv"
)

// A lead, end to end: the opening words of one reply put in the ghost's
// mouth, from `haunt say -lead` over the control socket and from `/lead`
// in the input line, on the fake model.

// leadRules: events are silent, the questions are answered through a
// speak call, so a lead is the speak call's prefill.
func leadRules() []fake.Rule {
	return rules(
		[]fake.Rule{silentOn("[haunt event]")},
		says("what do you think", ", the kettle is plotting something."),
		says("and the toaster", "Innocent, for now."),
		says("", "Mm."),
	)
}

// speakPrefills is the prefill each speak call carried, in order: the
// trailing assistant message of every call offered no tools.
func speakPrefills(t *testing.T, rig brainRig) []string {
	t.Helper()
	var out []string
	for _, body := range rig.Fake.Bodies() {
		var req proxyRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		if len(req.Tools) > 0 || !strings.Contains(string(body), speakCue) {
			continue
		}
		prefill := ""
		if n := len(req.Messages); n > 0 && req.Messages[n-1].Role == "assistant" {
			prefill = contentText(req.Messages[n-1].Content)
		}
		out = append(out, prefill)
	}
	return out
}

// TestLeadSaid: `haunt say -lead` reaches the speak call as its prefill,
// the words open with it, the decide call's context asks for it, history
// -why and -json say the ghost was led, and the next turn is not led.
func TestLeadSaid(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	rig := newBrainRig(t, dir, leadRules(), "")
	gDir := filepath.Join(dir, "wisp")
	writeGhost(t, gDir, wisp)
	state := filepath.Join(dir, "state")
	rig.start(t, c, gDir, state)
	waitFakeLog(t, rig.FakeLog, 30*time.Second, repliedTo("kind=boot"))

	sc := exec.Command(rig.Bin, "say", "-ghost", "wisp", "-lead", "Well, actually", "what do you think?")
	sc.Env = append(os.Environ(), rig.env()...)
	if out, err := sc.CombinedOutput(); err != nil {
		t.Fatalf("say -lead: %v: %s", err, out)
	}
	chat := waitFakeLog(t, rig.FakeLog, 30*time.Second, turnsAbout("what do you think"))
	if !strings.HasSuffix(chat.Context, `Begin your reply with: "Well, actually"`) {
		t.Errorf("the led turn's context ends %q", chat.Context)
	}
	reply := waitFakeLog(t, rig.FakeLog, 10*time.Second, repliedTo("what do you think"))
	if reply.Reply != "Well, actually, the kettle is plotting something." {
		t.Errorf("led reply = %q, want it opened by the lead", reply.Reply)
	}
	if got := speakPrefills(t, rig); len(got) != 1 || got[0] != "Well, actually" {
		t.Errorf("speak prefills = %q, want the lead on the one speak call", got)
	}

	rig.say(t, "and the toaster?")
	next := waitFakeLog(t, rig.FakeLog, 30*time.Second, turnsAbout("and the toaster"))
	if strings.Contains(next.Context, "Begin your reply with") {
		t.Errorf("the turn after the lead carries the hint: %q", next.Context)
	}
	if r := waitFakeLog(t, rig.FakeLog, 10*time.Second, repliedTo("and the toaster")); r.Reply != "Innocent, for now." {
		t.Errorf("the turn after = %q, want no lead", r.Reply)
	}
	if got := speakPrefills(t, rig); len(got) != 2 || got[1] != "" {
		t.Errorf("speak prefills = %q, want none on the second", got)
	}

	var out string
	deadline := time.Now().Add(10 * time.Second)
	for out = rig.history(t, state, "-why"); !strings.Contains(out, "Innocent, for now."); out = rig.history(t, state, "-why") {
		if time.Now().After(deadline) {
			t.Fatalf("history lacks the second reply:\n%s", out)
		}
		wait100ms()
	}
	if !strings.Contains(out, "ghost: Well, actually, the kettle is plotting something.\n       · led with \"Well, actually\"\n") {
		t.Errorf("history -why does not show the lead under the led reply:\n%s", out)
	}
	if n := strings.Count(out, "led with"); n != 1 {
		t.Errorf("history -why shows %d leads, want 1:\n%s", n, out)
	}
	turns := historyJSON(t, rig, state)
	i := slices.IndexFunc(turns, func(r traceRecord) bool { return strings.HasPrefix(r.Input, "what do you think") })
	if i < 0 || turns[i].Led != "Well, actually" || turns[i].LedBy != "prefill" {
		t.Errorf("history -json led turn = %+v", turns)
	}
	if j := slices.IndexFunc(turns, func(r traceRecord) bool { return strings.HasPrefix(r.Input, "and the toaster") }); j < 0 || turns[j].Led != "" {
		t.Errorf("history -json turn after = %+v", turns)
	}
}

// TestLeadInTheBox: `/lead Well, actually` in the input line sets the lead
// and sends nothing; the next line goes with it, and the one after that
// without.
func TestLeadInTheBox(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	rig := newBrainRig(t, dir, leadRules(), "[log]\ndebug = [\"input\"]\n")
	gDir := filepath.Join(dir, "wisp")
	writeGhost(t, gDir, wisp)
	kb, err := c.NewKeyboard()
	if err != nil {
		t.Fatalf("virtual keyboard: %v", err)
	}
	defer kb.Close()
	_, stderr := rig.start(t, c, gDir, filepath.Join(dir, "state"))
	waitFakeLog(t, rig.FakeLog, 30*time.Second, repliedTo("kind=boot"))

	ptr, err := c.NewPointer()
	if err != nil {
		t.Fatal(err)
	}
	defer ptr.Close()
	if err := ptr.MoveTo(180, 180); err != nil {
		t.Fatal(err)
	}
	if err := ptr.RightClick(); err != nil {
		t.Fatal(err)
	}
	waitLog(t, stderr, "input line opened")
	line := func(s string) {
		t.Helper()
		if err := kb.Type(s); err != nil {
			t.Fatal(err)
		}
		if err := kb.Key(keyEnter); err != nil {
			t.Fatal(err)
		}
	}
	line("/lead Well, actually")
	waitLog(t, stderr, `lead set: "Well, actually"`)
	line("what do you think?")
	reply := waitFakeLog(t, rig.FakeLog, 30*time.Second, repliedTo("what do you think"))
	if reply.Reply != "Well, actually, the kettle is plotting something." {
		t.Errorf("led reply = %q", reply.Reply)
	}
	line("and the toaster?")
	if r := waitFakeLog(t, rig.FakeLog, 30*time.Second, repliedTo("and the toaster")); r.Reply != "Innocent, for now." {
		t.Errorf("the line after = %q, want no lead", r.Reply)
	}
	if got := speakPrefills(t, rig); len(got) != 2 || got[0] != "Well, actually" || got[1] != "" {
		t.Errorf("speak prefills = %q", got)
	}
	if n := len(fakeEntries(rig.FakeLog, turnsAbout("/lead"))); n != 0 {
		t.Errorf("the /lead line reached the model %d times", n)
	}
	if strings.Contains(stderr.String(), "conversation ended") {
		t.Error("the conversation ended during the exchanges")
	}
}
