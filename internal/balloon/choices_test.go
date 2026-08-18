package balloon

import (
	"image"
	"strings"
	"testing"
)

// The rows are the whole point: a click has to land on one, so their
// rectangles must be inside the panel, ordered, and not overlapping.
func TestChoiceRowsAreUsableTargets(t *testing.T) {
	r, err := NewRenderer(DefaultTheme())
	if err != nil {
		t.Fatal(err)
	}
	sp, rows := r.RenderChoices("What would you like?",
		[]string{"Have a look at my screen", "Draw something", "Nothing, thanks"}, -1)

	if len(rows) != 3 {
		t.Fatalf("got %d rows for 3 choices", len(rows))
	}
	w, h := sp.Size()
	panel := image.Rect(0, 0, w, h)
	for i, row := range rows {
		if !row.In(panel) {
			t.Errorf("row %d %v is outside the %v panel", i, row, panel)
		}
		if row.Dy() < 12 {
			t.Errorf("row %d is %dpx tall; nobody can click that", i, row.Dy())
		}
		if i > 0 {
			if row.Min.Y < rows[i-1].Max.Y {
				t.Errorf("row %d overlaps row %d: %v, %v", i, i-1, row, rows[i-1])
			}
		}
	}
	// The question is above the answers, not on top of them.
	if rows[0].Min.Y <= 0 {
		t.Errorf("the first row starts at %d; the question has nowhere to go", rows[0].Min.Y)
	}
}

// The highlight is how the keyboard shows where it is, so it has to be
// visible — and only on the row selected.
func TestHighlightMarksOneRow(t *testing.T) {
	r, err := NewRenderer(DefaultTheme())
	if err != nil {
		t.Fatal(err)
	}
	plain, rows := r.RenderChoices("Pick one", []string{"first", "second"}, -1)
	lit, _ := r.RenderChoices("Pick one", []string{"first", "second"}, 1)

	differs := func(row image.Rectangle) bool {
		for y := row.Min.Y; y < row.Max.Y; y++ {
			for x := row.Min.X; x < row.Max.X; x++ {
				if plain.Img.NRGBAAt(x, y) != lit.Img.NRGBAAt(x, y) {
					return true
				}
			}
		}
		return false
	}
	if differs(rows[0]) {
		t.Error("highlighting the second row changed the first")
	}
	if !differs(rows[1]) {
		t.Error("the highlighted row looks exactly like an unhighlighted one")
	}
}

// A menu is a balloon: it must not grow past the panel width, however long
// the options are.
func TestLongChoicesStayInsideThePanel(t *testing.T) {
	th := DefaultTheme()
	r, err := NewRenderer(th)
	if err != nil {
		t.Fatal(err)
	}
	sp, rows := r.RenderChoices("Well?", []string{strings.Repeat("a very long option ", 20)}, -1)
	w, _ := sp.Size()
	if w > th.MaxWidth {
		t.Errorf("panel is %dpx wide, theme allows %d", w, th.MaxWidth)
	}
	if rows[0].Max.X > w {
		t.Errorf("row %v runs past the panel edge %d", rows[0], w)
	}
}

// Numbering is the keyboard path made visible: without it, "press 2" is a
// secret.
func TestChoicesAreNumbered(t *testing.T) {
	r, err := NewRenderer(DefaultTheme())
	if err != nil {
		t.Fatal(err)
	}
	// Rendered at two sizes: the numbers are drawn, so this checks the
	// layout leaves room for them rather than reading the pixels.
	narrow, rows := r.RenderChoices("?", []string{"yes", "no"}, -1)
	wide, _ := r.RenderChoices("?", []string{"yes", "no"}, -1)
	if narrow.Img.Bounds() != wide.Img.Bounds() {
		t.Error("the same menu rendered twice at different sizes")
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d", len(rows))
	}
}

// At HiDPI everything scales together, including the rows a click is
// matched against.
func TestChoiceRowsScale(t *testing.T) {
	one, err := NewRendererScaled(DefaultTheme(), 1)
	if err != nil {
		t.Fatal(err)
	}
	two, err := NewRendererScaled(DefaultTheme(), 2)
	if err != nil {
		t.Fatal(err)
	}
	_, small := one.RenderChoices("Pick", []string{"a", "b"}, -1)
	_, big := two.RenderChoices("Pick", []string{"a", "b"}, -1)
	if big[0].Dy() < small[0].Dy()*3/2 {
		t.Errorf("rows did not scale: %v at 1x, %v at 2x", small[0], big[0])
	}
}
