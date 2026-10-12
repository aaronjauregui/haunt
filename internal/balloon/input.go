package balloon

import (
	"image"
	"image/color"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"

	"github.com/aaronjauregui/haunt/internal/sprite"
)

// The input box: what the person is typing, behind a "> " prompt. It is
// the full width of the panel, so typing never changes its width, and as
// tall as the text needs: each line the person made (a backslash before
// Enter) starts a row, a line too long for the width wraps onto more, and
// past InputRows rows it scrolls inside itself to keep the caret in view,
// with a dim mark at the edge where rows are hidden.

// InputRows is the most rows the input box grows to before it scrolls:
// enough to see a short paragraph being written, few enough that the box
// never crowds out the conversation above it.
const InputRows = 6

// InputBox is the text being typed, as the input box is to draw it.
type InputBox struct {
	Text string
	// Cursor is the caret's rune index into Text.
	Cursor int
	// Caret is whether the caret is drawn: it blinks.
	Caret bool
	// Top is the first row shown when the text has more rows than fit,
	// as the last draw left it; the box scrolls from there only as far
	// as the caret needs, so moving within what is shown moves nothing.
	Top int
}

// inputRow is one row of the box: its text, and the rune index in the
// whole text where it starts.
type inputRow struct {
	text  string
	start int
}

// promptText is drawn before the first row; later rows are indented by
// its width so the text lines up beneath it.
const promptText = "> "

// inputLayout breaks text into the box's rows, wrapping at a space where
// one fits and mid-word where none does, and says which row the cursor
// is on and how far along it the caret goes.
func (r *Renderer) inputLayout(text string, cursor int) (rows []inputRow, caretRow, caretX int) {
	t := r.theme
	indent := font.MeasureString(r.face, promptText).Ceil()
	width := t.MaxWidth - 2*t.Padding - indent - r.caretWidth()
	adv := func(c rune) fixed.Int26_6 {
		a, _ := r.face.GlyphAdvance(c)
		return a
	}
	runes := []rune(text)
	cursor = min(max(cursor, 0), len(runes))
	limit := fixed.I(max(width, 1))
	start := 0
	for start <= len(runes) {
		end := start
		for end < len(runes) && runes[end] != '\n' {
			end++
		}
		// One line of the person's, from start to end, in rows.
		row := start
		for {
			var w fixed.Int26_6
			brk, lastSpace := row, -1
			for brk < end {
				w += adv(runes[brk])
				if w > limit && brk > row {
					break
				}
				if runes[brk] == ' ' {
					lastSpace = brk
				}
				brk++
			}
			next := brk
			if brk < end && lastSpace >= row {
				brk, next = lastSpace+1, lastSpace+1
			}
			rows = append(rows, inputRow{text: string(runes[row:brk]), start: row})
			if next >= end {
				break
			}
			row = next
		}
		start = end + 1
	}
	for i, row := range rows {
		if row.start <= cursor {
			caretRow = i
		}
	}
	row := rows[caretRow]
	n := min(cursor-row.start, len([]rune(row.text)))
	caretX = indent + font.MeasureString(r.face, string([]rune(row.text)[:n])).Ceil()
	return rows, caretRow, caretX
}

// caretWidth is how wide the caret is drawn.
func (r *Renderer) caretWidth() int { return max(r.lineHeight()/12, 1) }

// inputWindow is which rows of the box are shown, top and how many: up
// to InputRows, starting from top moved only as far as keeps the caret
// row in view.
func inputWindow(rows, caretRow, top int) (int, int) {
	shown := min(rows, InputRows)
	top = min(max(top, caretRow-shown+1), caretRow)
	return min(max(top, 0), rows-shown), shown
}

// InputHeight is how many rows the box shows for in: the height it takes
// in the panel, in lines.
func (r *Renderer) InputHeight(in InputBox) int {
	rows, _, _ := r.inputLayout(in.Text, in.Cursor)
	return min(len(rows), InputRows)
}

// drawInput paints the box's rows into img with the first row's top at
// y, and reports the first row shown.
func (r *Renderer) drawInput(img *image.NRGBA, y int, in InputBox) int {
	t := r.theme
	lh := r.lineHeight()
	rows, caretRow, caretX := r.inputLayout(in.Text, in.Cursor)
	top, shown := inputWindow(len(rows), caretRow, in.Top)
	ascent := r.face.Metrics().Ascent.Ceil()
	indent := font.MeasureString(r.face, promptText).Ceil()
	d := &font.Drawer{Dst: img, Src: image.NewUniform(t.Text), Face: r.face}
	for i := top; i < top+shown; i++ {
		ry := y + (i-top)*lh
		x := t.Padding + indent
		if i == 0 {
			d.Dot = fixed.P(t.Padding, ry+ascent)
			d.DrawString(promptText)
		}
		d.Dot = fixed.P(x, ry+ascent)
		d.DrawString(strings.TrimRight(rows[i].text, " "))
		if in.Caret && i == caretRow {
			cx := t.Padding + caretX
			m := r.face.Metrics()
			top := ry + ascent - m.Ascent.Ceil() + 1
			bot := ry + ascent + m.Descent.Ceil()
			fillRect(img, image.Rect(cx, top, cx+r.caretWidth(), bot), t.Text)
		}
	}
	right := t.MaxWidth - t.Padding
	if top > 0 {
		r.moreMark(img, right, y+lh/2, true)
	}
	if top+shown < len(rows) {
		r.moreMark(img, right, y+(shown-1)*lh+lh/2, false)
	}
	return top
}

// RenderInput paints the box alone: the panel a line the driver opened
// for a question of its own is drawn in, and a conversation's before
// anything has been said. top is the first row it showed, for the next
// draw's InputBox.Top.
func (r *Renderer) RenderInput(in InputBox) (sp *sprite.Sprite, top int) {
	t := r.theme
	lh := r.lineHeight()
	img := image.NewNRGBA(image.Rect(0, 0, t.MaxWidth, r.InputHeight(in)*lh+2*t.Padding))
	paintPanel(img, t)
	top = r.drawInput(img, t.Padding, in)
	return &sprite.Sprite{Img: img}, top
}

// moreMark is a small dim triangle ending at x, centred on y, pointing up
// or down: there is more that way than is shown. A shape rather than a
// glyph, so it does not depend on what the face has.
func (r *Renderer) moreMark(img *image.NRGBA, x, y int, up bool) {
	h := max(r.lineHeight()/4, 2)
	c := r.dim()
	for i := range h {
		// Row i from the tip: up points at the top.
		row := y - h/2 + i
		if !up {
			row = y + h/2 - i
		}
		fillRect(img, image.Rect(x-h-i, row, x-h+i+1, row+1), c)
	}
}

// fillRect blends c over rect in img.
func fillRect(img *image.NRGBA, rect image.Rectangle, c color.NRGBA) {
	rect = rect.Intersect(img.Bounds())
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			img.SetNRGBA(x, y, blend(img.NRGBAAt(x, y), c, float64(c.A)/255))
		}
	}
}
