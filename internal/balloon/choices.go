package balloon

import (
	"fmt"
	"image"
	"image/color"
	"strings"
	"unicode/utf8"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"

	"github.com/aaronjauregui/haunt/internal/sprite"
)

// A balloon you can answer.
//
// Choices are the load-bearing part of balloon markup, not rich text. Menus
// are how classic ukagaka ghosts are mostly *operated*, and without them the
// only ways to address a ghost are clicking it and typing free text at it.
// The rows are numbered because the digits are the keyboard path to the same
// thing — the first interaction in this project that does not need a mouse.

// RenderChoices paints a question: the text, then the options beneath it,
// one to a row. It returns the rows' rectangles in the panel's own pixels,
// so the caller can tell which one a click landed on.
//
// highlight is the row drawn as selected (-1 for none), which is how the
// keyboard shows where it is.
func (r *Renderer) RenderChoices(text string, choices []string, highlight int) (*sprite.Sprite, []image.Rectangle) {
	t := r.theme
	metrics := r.face.Metrics()
	lineHeight := (metrics.Ascent + metrics.Descent).Ceil() + 2
	rowHeight := lineHeight + t.Padding/2

	inner := t.MaxWidth - 2*t.Padding
	lines := r.fit(r.wrap(text, inner), lineHeight)

	textWidth := 0
	for _, l := range lines {
		if w := font.MeasureString(r.face, l).Ceil(); w > textWidth {
			textWidth = w
		}
	}
	// The number is part of the label's width, so a long option is trimmed
	// rather than drawn off the edge of the panel.
	labels := make([]string, len(choices))
	for i, c := range choices {
		label := fmt.Sprintf("%d. %s", i+1, strings.TrimSpace(c))
		for font.MeasureString(r.face, label).Ceil() > inner && len(label) > 4 {
			_, size := utf8.DecodeLastRuneInString(label)
			label = label[:len(label)-size]
		}
		labels[i] = label
		if w := font.MeasureString(r.face, label).Ceil(); w > textWidth {
			textWidth = w
		}
	}

	w := textWidth + 2*t.Padding
	h := lineHeight*len(lines) + rowHeight*len(labels) + 2*t.Padding
	if len(labels) > 0 {
		h += t.Padding / 2 // a breath between the question and the answers
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	paintPanel(img, t)

	d := &font.Drawer{Dst: img, Src: image.NewUniform(t.Text), Face: r.face}
	y := t.Padding + metrics.Ascent.Ceil()
	for _, l := range lines {
		d.Dot = fixed.P(t.Padding, y)
		d.DrawString(l)
		y += lineHeight
	}
	y += t.Padding / 2

	rows := make([]image.Rectangle, len(labels))
	for i, label := range labels {
		top := y - metrics.Ascent.Ceil()
		rows[i] = image.Rect(t.Padding/2, top, w-t.Padding/2, top+rowHeight)
		if i == highlight {
			// The selected row is a lighter panel rather than inverted text:
			// it has to read as "this one" from the corner of an eye, over
			// whatever the desktop is doing behind it.
			fillRounded(img, rows[i], t.Radius/2, blend(t.Background, t.Border, 0.45))
		}
		// Drawn after the highlight, or the highlight would cover it.
		d.Dot = fixed.P(t.Padding, y)
		d.DrawString(label)
		y += rowHeight
	}
	return &sprite.Sprite{Img: img}, rows
}

// fillRounded paints a rounded rectangle, for the selected row.
func fillRounded(img *image.NRGBA, r image.Rectangle, rad int, c color.NRGBA) {
	if rad < 0 {
		rad = 0
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			cx, cy := -1, -1
			if x < r.Min.X+rad {
				cx = r.Min.X + rad - 1
			} else if x >= r.Max.X-rad {
				cx = r.Max.X - rad
			}
			if y < r.Min.Y+rad {
				cy = r.Min.Y + rad - 1
			} else if y >= r.Max.Y-rad {
				cy = r.Max.Y - rad
			}
			if cx >= 0 && cy >= 0 {
				dx, dy := x-cx, y-cy
				if dx*dx+dy*dy > rad*rad {
					continue
				}
			}
			if (image.Point{X: x, Y: y}).In(img.Bounds()) {
				img.SetNRGBA(x, y, c)
			}
		}
	}
}

// blend mixes two colours, for the selected row's background.
func blend(a, b color.NRGBA, f float64) color.NRGBA {
	mix := func(x, y uint8) uint8 { return uint8(float64(x)*(1-f) + float64(y)*f) }
	return color.NRGBA{R: mix(a.R, b.R), G: mix(a.G, b.G), B: mix(a.B, b.B), A: a.A}
}
