package ghost

import (
	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/proto"
)

// Paging. A reply longer than the balloon used to be unreadable: the
// opening scrolled away while it streamed and there was no way back to it,
// at any token rate. While text is still arriving the balloon follows the
// tail, because the newest words are the ones being spoken; once it has all
// arrived the balloon shows the beginning and a click brings the next page.
//
// Clicks are the gesture because the ghost already has them and a balloon
// takes no pointer input of its own (it is pass-through, so clicks land on
// whatever is beneath). While a paged balloon is showing, a click on the
// ghost turns the page instead of reaching the brain — the same rule the
// typed-input line uses, for the same reason: one gesture, and the thing on
// screen has first claim on it.

// paged is a long line the user is reading a page at a time.
type paged struct {
	text  string
	page  int
	pages int
}

// showPaged puts a finished line on screen from its beginning, paging if it
// does not fit.
func (g *Ghost) showPaged(text string) {
	if muted, why := g.quiet.Active(); muted {
		g.log.Debug(hlog.Balloon, "balloon suppressed (%s)", why)
		return
	}
	if g.holdLine(text, nil) {
		return
	}
	// Past both suppressions, so this line is one a bystander could read —
	// which is the test for whether the other ghosts in the room get to
	// hear it (sociality.go). Every finished line the ghost says arrives
	// here, scripted or generated, so this is the one place it is minted.
	g.announce(proto.ActedSpoke, text)
	sp, pages := g.renderer().RenderPage(text, 0)

	g.pageMu.Lock()
	if pages > 1 {
		g.paged = &paged{text: text, page: 0, pages: pages}
	} else {
		g.paged = nil
	}
	g.pageMu.Unlock()

	g.showBalloonSprite(sp)
}

// turnPage advances a paged balloon, reporting whether it consumed the
// gesture. The last page dismisses the balloon, so a reader is never left
// clicking at a ghost that has stopped responding to clicks.
func (g *Ghost) turnPage() bool {
	g.pageMu.Lock()
	p := g.paged
	if p == nil {
		g.pageMu.Unlock()
		return false
	}
	p.page++
	done := p.page >= p.pages
	if done {
		g.paged = nil
	}
	text, page := p.text, p.page
	g.pageMu.Unlock()

	if done {
		g.hideBalloon()
		return true
	}
	sp, _ := g.renderer().RenderPage(text, page)
	g.showBalloonSprite(sp)
	return true
}

// clearPaging forgets any paged text, for when the balloon is replaced or
// dismissed by something other than a page turn.
func (g *Ghost) clearPaging() {
	g.pageMu.Lock()
	g.paged = nil
	g.pageMu.Unlock()
}
