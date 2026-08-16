package ghost

import (
	"strings"
	"testing"

	"github.com/aaronjauregui/haunt/internal/balloon"
	"github.com/aaronjauregui/haunt/internal/bus"
	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/quiet"
)

// pagingGhost renders balloons for real but has no compositor to show them
// on, which is enough to drive the page state.
func pagingGhost(t *testing.T) *Ghost {
	t.Helper()
	theme := balloon.DefaultTheme()
	theme.MaxHeight = 200
	r, err := balloon.NewRenderer(theme)
	if err != nil {
		t.Fatal(err)
	}
	q, err := quiet.Load()
	if err != nil {
		t.Fatal(err)
	}
	return &Ghost{
		opts:    Options{},
		log:     hlog.Discard(),
		blnRend: r,
		quiet:   q,
		bus:     bus.New(64),
	}
}

var longLine = strings.Repeat("The ghost considered the matter at length. ", 30)

// TestPagingAdvancesAndEnds: a long reply is readable from the beginning,
// a click brings the next page, and the last click puts it away — so the
// reader is never left clicking at a ghost that has stopped answering.
func TestPagingAdvancesAndEnds(t *testing.T) {
	g := pagingGhost(t)
	g.showPaged(longLine)

	g.pageMu.Lock()
	pages := g.paged.pages
	g.pageMu.Unlock()
	if pages < 3 {
		t.Fatalf("long text made %d pages, want several", pages)
	}

	for i := 1; i < pages; i++ {
		if !g.turnPage() {
			t.Fatalf("page %d did not advance", i)
		}
		g.pageMu.Lock()
		at := g.paged.page
		g.pageMu.Unlock()
		if at != i {
			t.Errorf("after %d turns the balloon is on page %d", i, at)
		}
	}

	// The turn past the last page consumes the click and dismisses.
	if !g.turnPage() {
		t.Error("the final turn did not consume the click")
	}
	g.pageMu.Lock()
	done := g.paged == nil
	g.pageMu.Unlock()
	if !done {
		t.Error("the balloon is still paging after its last page")
	}
	// And now clicks belong to the brain again.
	if g.turnPage() {
		t.Error("a dismissed balloon still swallowed a click")
	}
}

// TestShortLineDoesNotPage: an ordinary remark must not turn the next click
// into a page turn, or every ghost would swallow its own interactions.
func TestShortLineDoesNotPage(t *testing.T) {
	g := pagingGhost(t)
	g.showPaged("Boo.")
	if g.turnPage() {
		t.Error("a one-page balloon swallowed a click")
	}
}

// TestNewLineReplacesPaging: saying something else abandons whatever was
// being read, rather than leaving a stale page count behind.
func TestNewLineReplacesPaging(t *testing.T) {
	g := pagingGhost(t)
	g.showPaged(longLine)
	g.showPaged("Never mind.")
	if g.turnPage() {
		t.Error("the old paged text is still claiming clicks")
	}
}

// TestClearPaging: hide_say drops the pages too.
func TestClearPaging(t *testing.T) {
	g := pagingGhost(t)
	g.showPaged(longLine)
	g.clearPaging()
	if g.turnPage() {
		t.Error("cleared paging still claimed a click")
	}
}
