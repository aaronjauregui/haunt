package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/model/fake"
	"github.com/aaronjauregui/haunt/internal/testenv"
)

// A conversation in the default corner, end to end: a short reply first,
// then the conversation opened on it, three long exchanges and a second
// row in the box. The panel stands on the ghost's feet throughout, so the
// line being typed stays where it is and the transcript grows upward;
// nothing is drawn below the bottom of the screen. With HAUNT_SHOT_DIR
// set, a screenshot of each step is saved there, which is how the
// placement is looked at by eye.
func TestConversationGrowsUpward(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	long := func(n string) string {
		return "The " + n + " answer runs to a few sentences, as a conversation lets it. " +
			"It goes past one line and past two, so the panel has to grow to hold it, " +
			"and it ends here, with the " + n + " full stop."
	}
	rules := []fake.Rule{
		silentOn("[haunt event]"),
		{Match: "hello there", AfterTool: speakCue, Reply: "Hello."},
		{Match: "hello there", Calls: []fake.Call{{Name: "speak"}}},
	}
	for _, q := range []string{"first", "second", "third"} {
		rules = append(rules,
			fake.Rule{Match: q + " question", AfterTool: speakCue, Reply: long(q)},
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

	// Where defaultSpot puts a 160-pixel ghost on the 1280x720 headless
	// screen: the bottom right, inset 48.
	const spriteX, spriteY, sprite, screenH = 1072, 512, 160, 720
	const feet = spriteY + sprite
	_, stderr := rig.start(t, c, gDir, filepath.Join(dir, "state"),
		"-x", strconv.Itoa(spriteX), "-y", strconv.Itoa(spriteY))
	waitFake(t, rig.FakeLog, 1, 30*time.Second, repliedTo("kind=boot"))

	// The balloon is to the ghost's left here (balloonSide), so that is
	// where its navy is looked for.
	path := filepath.Join(dir, "place.png")
	shotDir := os.Getenv("HAUNT_SHOT_DIR")
	step := 0
	rows := func(what string, ok func(top, bottom int) bool) (int, int) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			top, bottom := panelRows(shot(t, c, path), spriteX-500, spriteX-4)
			if top >= 0 && ok(top, bottom) {
				if shotDir != "" {
					step++
					name := filepath.Join(shotDir, fmt.Sprintf("%d-%s.png", step, what))
					if err := c.Screenshot(name); err != nil {
						t.Errorf("screenshot: %v", err)
					}
				}
				t.Logf("%s: panel rows %d to %d", what, top, bottom)
				return top, bottom
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: panel rows %d to %d", what, top, bottom)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	anywhere := func(int, int) bool { return true }
	// The panel's last navy row is its own bottom edge, a pixel or two
	// inside the surface for the rounded corner and the border.
	onFeet := func(_, bottom int) bool { return bottom <= feet && bottom >= feet-4 }

	// A short reply outside a conversation hangs from the ghost's top.
	rig.say(t, "hello there")
	waitFake(t, rig.FakeLog, 1, 30*time.Second, repliedTo("hello there"))
	if top, _ := rows("reply", anywhere); top < spriteY || top > spriteY+4 {
		t.Errorf("the short reply's top is at %d, want it level with the ghost's top at %d", top, spriteY)
	}

	// The conversation opens on the ghost's feet.
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
	waitLog(t, stderr, "conversation=true")
	turns := strings.Count(stderr.String(), "turn over: person")
	last, _ := rows("opened", onFeet)

	// A second row in the box grows the panel upward by a row: the row
	// being typed stays where it was. The question that follows is typed
	// beneath it, so the first message is two lines.
	if err := kb.Type(`one row\`); err != nil {
		t.Fatal(err)
	}
	if err := kb.Key(keyEnter); err != nil {
		t.Fatal(err)
	}
	last, _ = rows("two-rows", func(top, bottom int) bool {
		return onFeet(top, bottom) && top <= last-10 && top >= last-40
	})

	for i, q := range []string{"first", "second", "third"} {
		if err := kb.Type("the " + q + " question"); err != nil {
			t.Fatal(err)
		}
		if err := kb.Key(keyEnter); err != nil {
			t.Fatal(err)
		}
		waitFake(t, rig.FakeLog, 1, 30*time.Second, repliedTo(q+" question"))
		waitLogCount(t, stderr, "turn over: person", turns+i+1)
		top, _ := rows(q, func(top, bottom int) bool { return onFeet(top, bottom) && top < last })
		last = top
	}

	// Every panel the ghost put up, as it placed it: the conversation's
	// stand on its feet, and none runs past the bottom of the screen.
	placed := regexp.MustCompile(`balloon at (-?\d+),(-?\d+), (\d+)x(\d+), conversation=(true|false)`).
		FindAllStringSubmatch(stderr.String(), -1)
	if len(placed) < 5 {
		t.Fatalf("%d balloons logged, want at least 5", len(placed))
	}
	for _, m := range placed {
		y, _ := strconv.Atoi(m[2])
		h, _ := strconv.Atoi(m[4])
		if y < 0 {
			t.Errorf("%s: above the top of the screen", m[0])
		}
		if y+h > screenH {
			t.Errorf("%s: runs to %d, past the bottom of the screen at %d", m[0], y+h, screenH)
		}
		if m[5] == "true" && y+h != feet && y != 0 {
			t.Errorf("%s: the conversation ends at %d, want it on the ghost's feet at %d", m[0], y+h, feet)
		}
	}
}
