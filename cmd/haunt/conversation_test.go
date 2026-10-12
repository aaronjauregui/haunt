package main

import (
	"image"
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

// Reading back through a long conversation and writing more than one
// line, end to end: the box grows a row for a backslash-Enter, and Enter
// sends both lines to the model with the newline; the wheel over the
// panel itself and the keyboard scroll the view; scrolled back, the view
// holds its place while the ghost answers a `haunt say` with a newline in
// it; and `haunt history` prints the two-line message readably.
func TestConversationReadBackAndNewLines(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	long := func(n string) string {
		return "The " + n + " answer is a long one, because a conversation is where a ghost may say " +
			"a few sentences. It goes on past one line and past two, so the view has to hold it. " +
			"There is more to say about it still, and the ghost says it, line after line, " +
			"until the answers together are more than the panel can show at once. " +
			"And it ends here, with the " + n + " full stop."
	}
	var rules []fake.Rule
	rules = append(rules, silentOn("[haunt event]"))
	for _, q := range []string{"first", "second", "third", "said"} {
		rules = append(rules,
			fake.Rule{Match: q + " line", AfterTool: speakCue, Reply: long(q)},
			fake.Rule{Match: q + " line", Calls: []fake.Call{{Name: "speak"}}})
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
	stateDir := filepath.Join(dir, "state")
	_, stderr := rig.start(t, c, gDir, stateDir)
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
	path := filepath.Join(dir, "box.png")
	// The panel stands on the ghost's feet and grows upward, so growing
	// is its top rising.
	top := func(below int) int {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			top, _ := panelRows(shot(t, c, path), spriteX+180, spriteX+640)
			if top >= 0 && top < below {
				return top
			}
			if time.Now().After(deadline) {
				t.Fatalf("the panel's top stayed at %d, want above %d", top, below)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	// The box grows a row for a backslash-Enter, and Enter sends both
	// lines as one message.
	one := top(1 << 20)
	if err := kb.Type(`the first line\`); err != nil {
		t.Fatal(err)
	}
	if err := kb.Key(keyEnter); err != nil {
		t.Fatal(err)
	}
	if two := top(one - 10); two < one-40 {
		t.Errorf("the box grew from a top at %d to %d for one row", one, two)
	}
	if err := kb.Type("goes on here"); err != nil {
		t.Fatal(err)
	}
	if err := kb.Key(keyEnter); err != nil {
		t.Fatal(err)
	}
	waitFake(t, rig.FakeLog, 1, 30*time.Second, repliedTo("the first line\ngoes on here"))
	waitLogCount(t, stderr, "turn over: person", 1)
	for i, q := range []string{"second", "third"} {
		if err := kb.Type("the " + q + " line"); err != nil {
			t.Fatal(err)
		}
		if err := kb.Key(keyEnter); err != nil {
			t.Fatal(err)
		}
		waitFake(t, rig.FakeLog, 1, 30*time.Second, repliedTo(q+" line"))
		waitLogCount(t, stderr, "turn over: person", i+2)
	}

	// The wheel over the panel, not the ghost, scrolls it.
	_, b := panelRows(shot(t, c, path), spriteX+180, spriteX+640)
	if err := ptr.MoveTo(spriteX+400, b-15); err != nil {
		t.Fatal(err)
	}
	if err := ptr.Scroll(-2); err != nil {
		t.Fatal(err)
	}
	waitLog(t, stderr, "conversation: 6 lines back")

	// And the keyboard: PageDown to the bottom, Shift+Up a step back.
	if err := kb.Key(pageDown); err != nil {
		t.Fatal(err)
	}
	waitLog(t, stderr, "conversation: 0 lines back")
	if err := kb.Chord(leftShift, arrowUp); err != nil {
		t.Fatal(err)
	}
	waitLog(t, stderr, "conversation: 3 lines back")
	if err := kb.Key(pageUp); err != nil {
		t.Fatal(err)
	}
	waitLogCount(t, stderr, "conversation: ", 4)

	// Scrolled back, the view holds its place while the ghost answers a
	// message from the command line, which keeps its newline too.
	rig.say(t, "a said line\nfrom the shell")
	waitFake(t, rig.FakeLog, 1, 30*time.Second, repliedTo("a said line\nfrom the shell"))
	waitLog(t, stderr, "conversation: held")
	if strings.Contains(stderr.String(), "conversation ended") {
		t.Error("the conversation ended")
	}
	if path := os.Getenv("HAUNT_SHOT"); path != "" {
		if err := c.Screenshot(path); err != nil {
			t.Errorf("screenshot: %v", err)
		}
	}

	if out := rig.history(t, stateDir); !strings.Contains(out, "you:   the first line\n       goes on here\n") ||
		!strings.Contains(out, "you:   a said line\n       from the shell\n") {
		t.Errorf("haunt history does not print the two-line messages as two lines:\n%s", out)
	}
}

// Keys the conversation tests press that type nothing.
const (
	leftShift = 42
	arrowUp   = 103
	pageUp    = 104
	pageDown  = 109
)

// A click on the ghost mid-conversation is a poke, and what the ghost
// says to it is one more of its lines in the view, like a reply: the
// panel grows by an entry, and the conversation goes on.
func TestConversationPoke(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	rules := []fake.Rule{
		silentOn("[haunt event]"),
		{Match: "question", AfterTool: speakCue, Reply: "An answer."},
		{Match: "question", Calls: []fake.Call{{Name: "speak"}}},
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
	if err := kb.Type("the question"); err != nil {
		t.Fatal(err)
	}
	if err := kb.Key(keyEnter); err != nil {
		t.Fatal(err)
	}
	waitFake(t, rig.FakeLog, 1, 30*time.Second, repliedTo("question"))
	waitLogCount(t, stderr, "turn over: person", 1)

	path := filepath.Join(dir, "poke.png")
	before := -1
	deadline := time.Now().Add(10 * time.Second)
	for before < 0 {
		if before, _ = panelRows(shot(t, c, path), spriteX+180, spriteX+640); before < 0 && time.Now().After(deadline) {
			t.Fatal("no conversation panel beside the ghost")
		}
	}
	if err := ptr.Press(); err != nil {
		t.Fatal(err)
	}
	if err := ptr.Release(); err != nil {
		t.Fatal(err)
	}
	// One entry is a line of text and the gap after it, and the panel
	// grows upward by it.
	deadline = time.Now().Add(10 * time.Second)
	for {
		after, _ := panelRows(shot(t, c, path), spriteX+180, spriteX+640)
		if after >= 0 && after <= before-20 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the panel's top is at %d after the poke, %d before: the poke's line is not in the view", after, before)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if strings.Contains(stderr.String(), "conversation ended") {
		t.Error("the poke ended the conversation")
	}
	if os.Getenv("HAUNT_SHOT") != "" {
		if err := c.Screenshot(os.Getenv("HAUNT_SHOT")); err != nil {
			t.Errorf("screenshot: %v", err)
		}
	}
}

// panelRows is the first and last rows of a balloon's navy between
// columns x0 and x1, anywhere on the screen, or -1, -1 when there is none.
func panelRows(img image.Image, x0, x1 int) (top, bottom int) {
	top, bottom = -1, -1
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := max(x0, img.Bounds().Min.X); x < min(x1, img.Bounds().Max.X); x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if r>>8 < 60 && g>>8 < 60 && b>>8 > 40 && b>>8 < 90 {
				if top < 0 {
					top = y
				}
				bottom = y
				break
			}
		}
	}
	return top, bottom
}
