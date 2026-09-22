package ghost

import (
	"fmt"
	"testing"
	"time"
)

// browsing reports which recalled line and page is on screen, if any.
func browsing(g *Ghost) (at, page int, ok bool) {
	g.recallMu.Lock()
	defer g.recallMu.Unlock()
	if g.recall.gen == 0 || g.recall.gen != g.balloonGen() || !g.blnShowing() {
		return 0, 0, false
	}
	return g.recall.at, g.recall.page, true
}

func wantBrowsing(t *testing.T, g *Ghost, at, page int) {
	t.Helper()
	gotAt, gotPage, ok := browsing(g)
	if !ok {
		t.Fatalf("not browsing; want line %d page %d", at, page)
	}
	if gotAt != at || gotPage != page {
		t.Fatalf("browsing line %d page %d, want line %d page %d", gotAt, gotPage, at, page)
	}
}

// TestRecallStepsBackAndForward: the wheel goes back through the ghost's
// lines and the person's alike, stops at the oldest, and past the newest
// puts the balloon away.
func TestRecallStepsBackAndForward(t *testing.T) {
	g := pagingGhost(t)
	g.showPaged("Boo.")
	g.remember(recalled{you: true, text: "hello"})
	g.showPaged("Oh, hello.")
	g.hideBalloon()

	g.onScroll(-1)
	wantBrowsing(t, g, 2, 0)
	g.onScroll(-1)
	wantBrowsing(t, g, 1, 0)
	g.onScroll(-5)
	wantBrowsing(t, g, 0, 0)

	g.onScroll(2)
	wantBrowsing(t, g, 2, 0)
	g.onScroll(1)
	if g.blnShowing() {
		t.Error("scrolling past the newest line left a balloon up")
	}
	if _, _, ok := browsing(g); ok {
		t.Error("still browsing after coming back to the present")
	}
}

// TestRecallStartsFromTheLineOnScreen: the line on screen is where the
// wheel starts, so it pages through a long reply both ways and then goes
// on into what came before it.
func TestRecallStartsFromTheLineOnScreen(t *testing.T) {
	g := pagingGhost(t)
	g.showPaged("Boo.")
	g.showPaged(longLine)
	pages := g.renderer().Pages(longLine)

	g.onScroll(1)
	wantBrowsing(t, g, 1, 1)
	g.onScroll(-1)
	wantBrowsing(t, g, 1, 0)
	g.onScroll(-1)
	wantBrowsing(t, g, 0, 0)
	// Forward again lands on the long line's first page, and back from
	// the line after it would land on its last.
	g.onScroll(1)
	wantBrowsing(t, g, 1, 0)
	g.onScroll(pages - 1)
	wantBrowsing(t, g, 1, pages-1)
}

// TestRecallPicksUpFromAClickedPage: paging by click and paging by wheel
// are the same pages.
func TestRecallPicksUpFromAClickedPage(t *testing.T) {
	g := pagingGhost(t)
	g.showPaged(longLine)
	g.turnPage()
	g.onScroll(1)
	wantBrowsing(t, g, 0, 2)
}

// TestNewLineEndsRecall: the present wins. A line arriving while the
// person is scrolling back replaces what they were reading, and the next
// turn of the wheel starts from it.
func TestNewLineEndsRecall(t *testing.T) {
	g := pagingGhost(t)
	g.showPaged("one")
	g.showPaged("two")
	g.onScroll(-1)
	wantBrowsing(t, g, 0, 0)

	g.showPaged("three")
	if _, _, ok := browsing(g); ok {
		t.Fatal("still browsing after the ghost said something new")
	}
	g.onScroll(-1)
	wantBrowsing(t, g, 1, 0)
}

// TestRecallPutAway: a click puts a recalled line away and is used up
// doing it; the next click belongs to the brain.
func TestRecallPutAway(t *testing.T) {
	g := pagingGhost(t)
	g.showPaged("one")
	g.hideBalloon()
	g.onScroll(-1)
	if !g.endRecall() {
		t.Fatal("a recalled line on screen did not take the click")
	}
	if g.blnShowing() {
		t.Error("the recalled line is still up")
	}
	if g.endRecall() {
		t.Error("a click with nothing recalled was taken anyway")
	}
}

// TestRecallLeavesItselfAfterTheLeave: moving the pointer off the ghost
// puts the recalled line away, a moment later.
func TestRecallLeavesItselfAfterTheLeave(t *testing.T) {
	g := pagingGhost(t)
	g.showPaged("one")
	g.hideBalloon()
	g.onScroll(-1)
	g.onHover(false)
	deadline := time.Now().Add(recallAfterLeave + 2*time.Second)
	for g.blnShowing() {
		if time.Now().After(deadline) {
			t.Fatal("the recalled line stayed after the pointer left")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestRecallWaitsForTheLine: the balloon belongs to a line being typed, so
// the wheel does nothing then.
func TestRecallWaitsForTheLine(t *testing.T) {
	g := pagingGhost(t)
	g.showPaged("one")
	g.hideBalloon()
	g.input.open = true
	g.onScroll(-1)
	if g.blnShowing() {
		t.Error("recall painted over an open input line")
	}
}

// TestRecallKeepsTheRecent: the list is bounded, oldest out first.
func TestRecallKeepsTheRecent(t *testing.T) {
	g := pagingGhost(t)
	for i := 0; i < recallKeep+10; i++ {
		g.remember(recalled{text: fmt.Sprint(i)})
	}
	g.recallMu.Lock()
	defer g.recallMu.Unlock()
	if n := len(g.recall.lines); n != recallKeep {
		t.Fatalf("kept %d lines, want %d", n, recallKeep)
	}
	if first := g.recall.lines[0].text; first != "10" {
		t.Errorf("oldest kept is %q, want %q", first, "10")
	}
}

func TestAgo(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Second: "just now",
		4 * time.Minute:  "4m ago",
		3 * time.Hour:    "3h ago",
		50 * time.Hour:   "2d ago",
	} {
		if got := ago(d); got != want {
			t.Errorf("ago(%v) = %q, want %q", d, got, want)
		}
	}
}
