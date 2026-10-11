package ghost

import (
	"fmt"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/sprite"
)

// Recall. A balloon is gone once it has lingered its time, and with it
// whatever the ghost said — so turning the wheel over the ghost goes back
// through the conversation: its lines, the pictures it hung, and what the
// person typed, a page at a time, with a footer saying who said each one,
// how long ago, and where it sits in the conversation. Turning the other way comes
// forward again, and past the newest line the balloon goes. Taking the
// pointer off the ghost puts the past away a moment later, and so does a
// click.
//
// The line on screen is where recall starts, so the wheel also pages
// through a long reply the way clicking does, and backwards too.
//
// Browsing is one particular balloon, known by its generation: anything
// the ghost says meanwhile replaces it, and that is the end of browsing.
// The present always wins over the past. The thinking indicator, which
// never replaces a balloon somebody is reading, leaves it alone.
//
// This is the session's memory of the screen, not the conversation's
// record — `haunt history` reads that from the transcript. It holds only what
// reached the screen, so a line quiet hours kept off it is not here.

// recallKeep is how many lines are kept to scroll back through.
const recallKeep = 50

// How long a recalled line stays: a moment after the pointer leaves the
// ghost, and longer while it sits there, in case the person has walked
// away with the pointer still on it.
const (
	recallAfterLeave = 2 * time.Second
	recallIdle       = 30 * time.Second
)

// recalled is one thing that was said: a line of the ghost's, a picture it
// showed, or a line the person typed.
type recalled struct {
	at      time.Time
	you     bool
	text    string
	picture string // the picture's ID, when this is a picture
	// gen is the balloon it was shown in, so the one on screen can be
	// found in the list; zero for what the person typed, which is never
	// a balloon of its own.
	gen uint64
}

// recallState is what has been said, and where browsing it has got to.
type recallState struct {
	lines []recalled // oldest first
	// gen is the balloon showing lines[at], page page, while browsing;
	// zero when not.
	gen      uint64
	at, page int
	timer    *time.Timer
}

// remember notes something that was said. In a conversation that is
// also what puts it in the view, at the bottom.
func (g *Ghost) remember(r recalled) {
	if r.at.IsZero() {
		r.at = time.Now()
	}
	g.recallMu.Lock()
	g.recall.lines = append(g.recall.lines, r)
	if n := len(g.recall.lines); n > recallKeep {
		g.recall.lines = append(g.recall.lines[:0:0], g.recall.lines[n-recallKeep:]...)
	}
	g.recallMu.Unlock()
	if g.conversationSaid() {
		g.drawInput()
	}
}

// recallMoved follows a line from one balloon to the next, for a page
// turn: the same line is on screen, in a new balloon.
func (g *Ghost) recallMoved(from, to uint64) {
	g.recallMu.Lock()
	defer g.recallMu.Unlock()
	for i := len(g.recall.lines) - 1; i >= 0; i-- {
		if g.recall.lines[i].gen == from {
			g.recall.lines[i].gen = to
			return
		}
	}
}

// recallUp marks the newest line the ghost said with text as the one in
// balloon to: the last words of a conversation, put up again as it ends.
// Found by its words rather than by its balloon, because the panel it was
// said in may have been replaced since by a taller one.
func (g *Ghost) recallUp(text string, to uint64) {
	g.recallMu.Lock()
	defer g.recallMu.Unlock()
	for i := len(g.recall.lines) - 1; i >= 0; i-- {
		if l := g.recall.lines[i]; !l.you && l.picture == "" && l.text == text {
			g.recall.lines[i].gen = to
			return
		}
	}
}

// onScroll steps through what was said: back for a negative count, forward
// for a positive one, a page at a time. In a conversation it moves the
// conversation's view instead, a few lines at a time (conversation.go).
func (g *Ghost) onScroll(steps int) {
	if g.clickInConversation() {
		g.scrollConversation(steps)
		return
	}
	// The balloon is taken while a line is being typed or a question is
	// waiting, and quiet hours mean no balloons at all.
	if g.inputOpen() || g.choiceOpen() {
		return
	}
	if muted, _ := g.quiet.Active(); muted {
		return
	}
	rend := g.renderer()
	if rend == nil {
		return
	}

	g.recallMu.Lock()
	lines := g.recall.lines
	n := len(lines)
	if n == 0 {
		g.recallMu.Unlock()
		return
	}
	pages := func(i int) int {
		if lines[i].picture != "" {
			return 1
		}
		return rend.Pages(lines[i].text)
	}
	at, page := g.recallFrom()
	from, fromPage := at, page
	for ; steps < 0; steps++ {
		switch {
		case page > 0:
			page--
		case at > 0:
			at--
			page = pages(at) - 1
		}
	}
	for ; steps > 0 && at < n; steps-- {
		if page < pages(at)-1 {
			page++
		} else {
			at, page = at+1, 0
		}
	}
	if at == from && page == fromPage {
		g.recallMu.Unlock()
		g.recallLinger(recallIdle)
		return
	}
	if at == n {
		// Forward past the newest line: back to the present, which has
		// nothing on screen.
		g.recall.gen = 0
		g.recallMu.Unlock()
		g.log.Debug(hlog.Balloon, "recall: back to the present")
		g.clearPaging()
		g.hideBalloon()
		return
	}
	line := lines[at]
	g.recallMu.Unlock()

	sp := g.renderRecalled(line, page, at, n)
	g.clearPaging()
	g.showBalloonSprite(sp)
	gen := g.balloonGen()

	g.recallMu.Lock()
	g.recall.gen, g.recall.at, g.recall.page = gen, at, page
	g.recallMu.Unlock()
	g.log.Debug(hlog.Balloon, "recall: line %d of %d, page %d", at+1, n, page+1)
	g.recallLinger(recallIdle)
}

// recallFrom is where a scroll starts: the recalled line being browsed, or
// the line on screen, or else the present — one past the newest line.
// Called with recallMu held.
func (g *Ghost) recallFrom() (at, page int) {
	lines := g.recall.lines
	if !g.blnShowing() {
		return len(lines), 0
	}
	gen := g.balloonGen()
	if g.recall.gen != 0 && g.recall.gen == gen {
		return g.recall.at, g.recall.page
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i].gen == gen {
			g.pageMu.Lock()
			if g.paged != nil {
				page = g.paged.page
			}
			g.pageMu.Unlock()
			return i, page
		}
	}
	return len(lines), 0
}

// renderRecalled paints one page of a recalled line, footer and all.
func (g *Ghost) renderRecalled(line recalled, page, at, n int) *sprite.Sprite {
	rend := g.renderer()
	// Who said it, when, and where it sits in what was said.
	who := "you"
	if !line.you {
		who = "ghost"
		if g.manifest != nil && g.manifest.Ghost.Name != "" {
			who = g.manifest.Ghost.Name
		}
	}
	footer := func(page, pages int) string {
		f := fmt.Sprintf("%s · %s · %d/%d", who, ago(time.Since(line.at)), at+1, n)
		if pages > 1 {
			f += fmt.Sprintf(" · p%d/%d", page+1, pages)
		}
		return f
	}
	if line.picture != "" {
		img, err := g.loadPicture(line.picture)
		if err == nil {
			return rend.RenderRecalledImage(img, footer(0, 1))
		}
		line.text = "(" + err.Error() + ")"
	}
	sp, _ := rend.RenderRecalled(line.text, page, footer)
	return sp
}

// ago says how long ago, as briefly as a footer wants it.
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
}

// onHover starts the countdown to putting a recalled line away when the
// pointer leaves the ghost, and relaxes it when the pointer comes back.
func (g *Ghost) onHover(inside bool) {
	if inside {
		g.recallLinger(recallIdle)
	} else {
		g.recallLinger(recallAfterLeave)
	}
}

// recallLinger puts the recalled line away after d, replacing any earlier
// countdown. Nothing is being browsed, nothing happens.
func (g *Ghost) recallLinger(d time.Duration) {
	g.recallMu.Lock()
	defer g.recallMu.Unlock()
	if g.recall.timer != nil {
		g.recall.timer.Stop()
		g.recall.timer = nil
	}
	gen := g.recall.gen
	if gen == 0 {
		return
	}
	g.recall.timer = time.AfterFunc(d, func() {
		g.guard(hlog.Balloon, "putting a recalled line away", func() {
			g.recallMu.Lock()
			still := g.recall.gen == gen
			g.recallMu.Unlock()
			if still {
				g.endRecall()
			}
		})
	})
}

// endRecall puts away a recalled line, reporting whether one was on
// screen to put away.
func (g *Ghost) endRecall() bool {
	g.recallMu.Lock()
	gen := g.recall.gen
	g.recall.gen = 0
	if g.recall.timer != nil {
		g.recall.timer.Stop()
		g.recall.timer = nil
	}
	g.recallMu.Unlock()
	if gen == 0 || g.balloonGen() != gen || !g.blnShowing() {
		return false
	}
	g.log.Debug(hlog.Balloon, "recall: put away")
	g.hideBalloon()
	return true
}
