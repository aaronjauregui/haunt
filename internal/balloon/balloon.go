// Package balloon renders dialogue balloons: word-wrapped text in a rounded
// panel, produced as an image for display on its own layer-shell surface.
// v1 is plain text on a fixed theme; markup and theme packages come later.
package balloon

import (
	"fmt"
	"image"
	"image/color"
	"strings"
	"unicode/utf8"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"github.com/aaronjauregui/haunt/internal/sprite"
)

// Theme is the balloon's visual configuration.
type Theme struct {
	Background color.NRGBA
	Text       color.NRGBA
	Border     color.NRGBA
	// MaxWidth is the widest a balloon grows before wrapping, in pixels.
	MaxWidth int
	// MaxHeight caps how tall a balloon may grow. Zero means unlimited,
	// which is only reasonable for tests: on a screen, a balloon that
	// outgrows the display stops being a balloon and becomes a wall.
	MaxHeight int
	FontSize  float64
	Padding   int
	Radius    int
}

// DefaultTheme is a soft dark panel with light text.
func DefaultTheme() Theme {
	return Theme{
		Background: color.NRGBA{R: 34, G: 36, B: 54, A: 235},
		Text:       color.NRGBA{R: 235, G: 235, B: 245, A: 255},
		Border:     color.NRGBA{R: 120, G: 130, B: 180, A: 255},
		MaxWidth:   320,
		MaxHeight:  420,
		FontSize:   16,
		Padding:    14,
		Radius:     12,
	}
}

// Renderer turns text into balloon sprites.
type Renderer struct {
	theme Theme
	face  font.Face
}

// NewRenderer prepares a renderer for the theme (parsing the embedded font).
func NewRenderer(theme Theme) (*Renderer, error) {
	ft, err := opentype.Parse(goregular.TTF)
	if err != nil {
		return nil, err
	}
	face, err := opentype.NewFace(ft, &opentype.FaceOptions{
		Size:    theme.FontSize,
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		return nil, err
	}
	return &Renderer{theme: theme, face: face}, nil
}

// Render lays out text and paints the balloon.
func (r *Renderer) Render(text string) *sprite.Sprite {
	t := r.theme
	maxTextWidth := t.MaxWidth - 2*t.Padding
	lines := r.wrap(text, maxTextWidth)

	metrics := r.face.Metrics()
	lineHeight := (metrics.Ascent + metrics.Descent).Ceil() + 2

	lines = r.fit(lines, lineHeight)

	return r.renderLines(lines, lineHeight)
}

// renderLines paints laid-out lines into a panel sized to them.
func (r *Renderer) renderLines(lines []string, lineHeight int) *sprite.Sprite {
	t := r.theme
	metrics := r.face.Metrics()

	textWidth := 0
	for _, l := range lines {
		if w := font.MeasureString(r.face, l).Ceil(); w > textWidth {
			textWidth = w
		}
	}
	w := textWidth + 2*t.Padding
	h := lineHeight*len(lines) + 2*t.Padding

	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	paintPanel(img, t)

	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(t.Text),
		Face: r.face,
	}
	y := t.Padding + metrics.Ascent.Ceil()
	for _, l := range lines {
		d.Dot = fixed.P(t.Padding, y)
		d.DrawString(l)
		y += lineHeight
	}
	return &sprite.Sprite{Img: img}
}

// RenderPage paints one screenful of a long line, and reports how many
// screenfuls there are. Page numbers past the end clamp to the last.
//
// This is the counterpart to the tail-following in Render: while dialogue is
// streaming, the newest words are what matters and the balloon behaves like
// a terminal; once it has all arrived, the reader wants the beginning, and
// wants the rest to still be reachable. Pagination is the smallest thing
// that makes a long reply readable at all — without it the opening scrolls
// away and there is no way back to it at any speed.
func (r *Renderer) RenderPage(text string, page int) (sp *sprite.Sprite, pages int) {
	t := r.theme
	lines := r.wrap(text, t.MaxWidth-2*t.Padding)
	metrics := r.face.Metrics()
	lineHeight := (metrics.Ascent + metrics.Descent).Ceil() + 2

	per := r.linesPerPage(lineHeight)
	pages = (len(lines) + per - 1) / per
	if pages < 1 {
		pages = 1
	}
	if page >= pages {
		page = pages - 1
	}
	if page < 0 {
		page = 0
	}

	start := page * per
	end := start + per
	if end > len(lines) {
		end = len(lines)
	}
	shown := append([]string{}, lines[start:end]...)
	if pages > 1 {
		// A quiet marker, so a reader knows there is more and that
		// clicking will bring it.
		shown = append(shown, fmt.Sprintf("— %d/%d —", page+1, pages))
	}
	return r.renderLines(shown, lineHeight), pages
}

// linesPerPage is how many lines fit, leaving room for the page marker.
func (r *Renderer) linesPerPage(lineHeight int) int {
	if r.theme.MaxHeight <= 0 {
		return 1 << 30
	}
	n := (r.theme.MaxHeight-2*r.theme.Padding)/lineHeight - 1
	if n < 1 {
		n = 1
	}
	return n
}

// RenderInput paints the line the user is typing, with a caret. It keeps a
// fixed width so the panel does not jitter with every keystroke, and shows
// the tail of a long line rather than the head — what you are typing now is
// what you want to see.
func (r *Renderer) RenderInput(text string, caretOn bool) *sprite.Sprite {
	t := r.theme
	metrics := r.face.Metrics()
	lineHeight := (metrics.Ascent + metrics.Descent).Ceil() + 2

	line := "> " + text
	inner := t.MaxWidth - 2*t.Padding
	// Drop leading runes until the tail fits, so the caret stays visible.
	for font.MeasureString(r.face, line+"|").Ceil() > inner && len(line) > 3 {
		_, size := utf8.DecodeRuneInString(line[2:])
		line = "> " + line[2+size:]
	}
	if caretOn {
		line += "|"
	}

	img := image.NewNRGBA(image.Rect(0, 0, t.MaxWidth, lineHeight+2*t.Padding))
	paintPanel(img, t)
	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(t.Text),
		Face: r.face,
		Dot:  fixed.P(t.Padding, t.Padding+metrics.Ascent.Ceil()),
	}
	d.DrawString(line)
	return &sprite.Sprite{Img: img}
}

// RenderImage paints an image inside a balloon panel, scaled down to fit
// MaxWidth if needed (nearest-neighbor; panels are small).
func (r *Renderer) RenderImage(src image.Image) *sprite.Sprite {
	t := r.theme
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	maxInner := t.MaxWidth - 2*t.Padding
	if w > maxInner {
		h = h * maxInner / w
		w = maxInner
	}
	panel := image.NewNRGBA(image.Rect(0, 0, w+2*t.Padding, h+2*t.Padding))
	paintPanel(panel, t)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sx := b.Min.X + x*b.Dx()/w
			sy := b.Min.Y + y*b.Dy()/h
			panel.Set(t.Padding+x, t.Padding+y, src.At(sx, sy))
		}
	}
	return &sprite.Sprite{Img: panel}
}

// fit trims wrapped text to the theme's height, keeping the tail. Dialogue
// arrives a token at a time and the newest words are the ones being said,
// so a balloon behaves like a terminal rather than a page: the ellipsis
// marks what scrolled off above. Proper paging belongs with balloon markup,
// which is a later item.
func (r *Renderer) fit(lines []string, lineHeight int) []string {
	if r.theme.MaxHeight <= 0 {
		return lines
	}
	max := (r.theme.MaxHeight - 2*r.theme.Padding) / lineHeight
	if max < 1 {
		max = 1
	}
	if len(lines) <= max {
		return lines
	}
	return append([]string{"…"}, lines[len(lines)-max+1:]...)
}

// wrap breaks text into lines no wider than maxWidth pixels, on spaces where
// possible. Explicit newlines are respected.
func (r *Renderer) wrap(text string, maxWidth int) []string {
	var lines []string
	for _, para := range strings.Split(text, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		cur := words[0]
		for _, word := range words[1:] {
			cand := cur + " " + word
			if font.MeasureString(r.face, cand).Ceil() > maxWidth {
				lines = append(lines, cur)
				cur = word
			} else {
				cur = cand
			}
		}
		lines = append(lines, cur)
	}
	return lines
}

// paintPanel fills a rounded rectangle with border into img.
func paintPanel(img *image.NRGBA, t Theme) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	rad := t.Radius
	if m := min(w, h) / 2; rad > m {
		rad = m
	}
	inside := func(x, y int) bool {
		// Corner circle test; edges and center are trivially inside.
		cx, cy := -1, -1
		if x < rad {
			cx = rad - 1
		} else if x >= w-rad {
			cx = w - rad
		}
		if y < rad {
			cy = rad - 1
		} else if y >= h-rad {
			cy = h - rad
		}
		if cx < 0 || cy < 0 {
			return true
		}
		dx, dy := x-cx, y-cy
		return dx*dx+dy*dy <= rad*rad
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if !inside(x, y) {
				continue
			}
			onEdge := x < 2 || y < 2 || x >= w-2 || y >= h-2 ||
				!inside(x-2, y) || !inside(x+2, y) || !inside(x, y-2) || !inside(x, y+2)
			if onEdge {
				img.SetNRGBA(x, y, t.Border)
			} else {
				img.SetNRGBA(x, y, t.Background)
			}
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
