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
	var before int
	deadline := time.Now().Add(10 * time.Second)
	for before == 0 {
		if before = balloonBottom(shot(t, c, path), spriteX, spriteY); before == 0 && time.Now().After(deadline) {
			t.Fatal("no conversation panel beside the ghost")
		}
	}
	if err := ptr.Press(); err != nil {
		t.Fatal(err)
	}
	if err := ptr.Release(); err != nil {
		t.Fatal(err)
	}
	// One entry is a line of text and the gap after it.
	deadline = time.Now().Add(10 * time.Second)
	for {
		after := balloonBottom(shot(t, c, path), spriteX, spriteY)
		if after >= before+20 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the panel's bottom is at %d after the poke, %d before: the poke's line is not in the view", after, before)
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

// balloonBottom is the lowest row of a balloon beside the sprite, or 0
// when there is none.
func balloonBottom(img image.Image, spriteX, spriteY int) int {
	bottom := 0
	for y := spriteY; y < img.Bounds().Max.Y; y++ {
		for x := spriteX + 180; x < min(spriteX+640, img.Bounds().Max.X); x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if r>>8 < 60 && g>>8 < 60 && b>>8 > 40 && b>>8 < 90 {
				bottom = y
				break
			}
		}
	}
	return bottom
}
