package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/model/fake"
	"github.com/aaronjauregui/haunt/internal/testenv"
)

// A conversation, end to end: three exchanges typed without reopening the
// line, the replies in one panel with the line beneath, and the wheel over
// the ghost going back through them without ending anything. With
// HAUNT_SHOT set to a path, a screenshot of the scrolled-back view is
// saved there, which is how the view is looked at by eye.
func TestConversationThreeExchanges(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	reply := func(n string) string {
		return "The " + n + " answer is a long one, because a conversation is where a ghost may say " +
			"a few sentences. It goes on past one line and past two, so the view has to hold it. " +
			"There is more to say about it still, and the ghost says it, line after line, " +
			"until the three of them together are more than the panel can show at once. " +
			"And it ends here, with the " + n + " full stop."
	}
	var rules []fake.Rule
	rules = append(rules, silentOn("[haunt event]"))
	for _, q := range []string{"first", "second", "third"} {
		rules = append(rules,
			fake.Rule{Match: q + " question", AfterTool: speakCue, Reply: reply(q)},
			fake.Rule{Match: q + " question", Calls: []fake.Call{{Name: "speak"}}})
	}
	rig := newBrainRig(t, dir, rules, "")
	gDir := filepath.Join(dir, "wisp")
	writeGhost(t, gDir, `[ghost]
name = "wisp"
capabilities = ["message", "llm"]

[persona]
system = "You are Wisp."
`)
	kb, err := c.NewKeyboard()
	if err != nil {
		t.Fatalf("virtual keyboard: %v", err)
	}
	defer kb.Close()
	_, stderr := rig.start(t, c, gDir, filepath.Join(dir, "state"))
	waitFake(t, rig.FakeLog, 1, 30*time.Second, repliedTo("kind=boot"))

	const spriteX, spriteY = 100, 100
	ptr, err := c.NewPointer()
	if err != nil {
		t.Fatal(err)
	}
	defer ptr.Close()
	if err := ptr.MoveTo(spriteX+80, spriteY+80); err != nil {
		t.Fatal(err)
	}
	if err := ptr.RightClick(); err != nil {
		t.Fatal(err)
	}
	for i, q := range []string{"first", "second", "third"} {
		if err := kb.Type("the " + q + " question"); err != nil {
			t.Fatal(err)
		}
		if err := kb.Key(keyEnter); err != nil {
			t.Fatal(err)
		}
		waitFake(t, rig.FakeLog, 1, 30*time.Second, repliedTo(q+" question"))
		waitLogCount(t, stderr, "turn over: person", i+1)
	}
	if strings.Contains(stderr.String(), "conversation ended") {
		t.Fatal("the conversation ended during the exchanges")
	}

	// The wheel goes back through the conversation, a few lines a step.
	if err := ptr.Scroll(-2); err != nil {
		t.Fatal(err)
	}
	waitLog(t, stderr, "conversation: 6 lines back")
	waitBalloon(t, c, dir, spriteX, spriteY, true)
	if path := os.Getenv("HAUNT_SHOT"); path != "" {
		if err := c.Screenshot(path); err != nil {
			t.Errorf("screenshot: %v", err)
		}
	}
	if strings.Contains(stderr.String(), "conversation ended") {
		t.Error("scrolling ended the conversation")
	}
}
