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
	// scale is the device pixels per logical pixel. Text is the part of a
	// balloon that suffers most from being drawn small and stretched, so
	// the whole panel is rendered at the screen's real resolution and the
	// surface's viewport declares the logical size it stands for.
	scale float64
}

// NewRenderer prepares a renderer for the theme (parsing the embedded font).
func NewRenderer(theme Theme) (*Renderer, error) {
	return NewRendererScaled(theme, 1)
}

// NewRendererScaled prepares a renderer that draws at scale device pixels
// per logical pixel: 2 for a 200% screen, 1.5 for 150%.
func NewRendererScaled(theme Theme, scale float64) (*Renderer, error) {
	if scale <= 0 {
		scale = 1
	}
	ft, err := opentype.Parse(goregular.TTF)
	if err != nil {
		return nil, err
	}
	scaled := theme
	scaled.FontSize = theme.FontSize * scale
	scaled.MaxWidth = int(float64(theme.MaxWidth) * scale)
	scaled.MaxHeight = int(float64(theme.MaxHeight) * scale)
	scaled.Padding = int(float64(theme.Padding) * scale)
	scaled.Radius = int(float64(theme.Radius) * scale)

	face, err := opentype.NewFace(ft, &opentype.FaceOptions{
		Size:    scaled.FontSize,
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		return nil, err
	}
	return &Renderer{theme: scaled, face: face, scale: scale}, nil
}

// Scale is the device pixels per logical pixel this renderer draws at.
func (r *Renderer) Scale() float64 {
	if r.scale <= 0 {
		return 1
	}
	return r.scale
}

// LogicalSize converts a rendered panel's pixel size back to the logical
// size it stands for, which is what the compositor needs to be told.
func (r *Renderer) LogicalSize(w, h int) (int, int) {
	s := r.Scale()
	return int(float64(w)/s + 0.5), int(float64(h)/s + 0.5)
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
	return r.renderLinesFooted(lines, lineHeight, false)
}

// renderLinesFooted is renderLines with the last line dimmed when footed:
// a marker about the line, which should read as one at a glance.
func (r *Renderer) renderLinesFooted(lines []string, lineHeight int, footed bool) *sprite.Sprite {
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
	for i, l := range lines {
		if footed && i == len(lines)-1 {
			d.Src = image.NewUniform(r.dim())
		}
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
	return r.renderPage(text, page, func(page, pages int) string {
		if pages < 2 {
			return ""
		}
		// A quiet marker, so a reader knows there is more and that
		// clicking will bring it.
		return fmt.Sprintf("— %d/%d —", page+1, pages)
	})
}

// RenderRecalled paints one page of a line from earlier in the
// conversation, with footer(page, pages) as its last line in place of the
// page marker: where the line sits in what was said, and when. Pages are
// counted exactly as RenderPage counts them, so a page of a line on screen
// is the same page when it is recalled.
func (r *Renderer) RenderRecalled(text string, page int, footer func(page, pages int) string) (sp *sprite.Sprite, pages int) {
	return r.renderPage(text, page, footer)
}

// Pages is how many pages RenderPage and RenderRecalled split text into.
func (r *Renderer) Pages(text string) int {
	lines := r.wrap(text, r.theme.MaxWidth-2*r.theme.Padding)
	per := r.linesPerPage(r.lineHeight())
	if pages := (len(lines) + per - 1) / per; pages > 1 {
		return pages
	}
	return 1
}

func (r *Renderer) lineHeight() int {
	metrics := r.face.Metrics()
	return (metrics.Ascent + metrics.Descent).Ceil() + 2
}

// renderPage lays out one page of text, ending with marker's line unless
// it is empty. Page numbers past the end clamp to the last.
func (r *Renderer) renderPage(text string, page int, marker func(page, pages int) string) (sp *sprite.Sprite, pages int) {
	t := r.theme
	lines := r.wrap(text, t.MaxWidth-2*t.Padding)
	lineHeight := r.lineHeight()

	per := r.linesPerPage(lineHeight)
	pages = r.Pages(text)
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
	m := marker(page, pages)
	if m != "" {
		shown = append(shown, m)
	}
	return r.renderLinesFooted(shown, lineHeight, m != ""), pages
}

// dim is the panel's text colour faded, for the lines about a balloon
// rather than in it: the page marker, and where a recalled line sits.
func (r *Renderer) dim() color.NRGBA {
	c := r.theme.Text
	c.A = 140
	return c
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

	img := image.NewNRGBA(image.Rect(0, 0, t.MaxWidth, lineHeight+2*t.Padding))
	paintPanel(img, t)
	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(t.Text),
		Face: r.face,
		Dot:  fixed.P(t.Padding, t.Padding+metrics.Ascent.Ceil()),
	}
	d.DrawString(r.inputLine(text, caretOn))
	return &sprite.Sprite{Img: img}
}

// inputLine is the typed line as drawn: a prompt, the tail that fits, and
// the caret when it is on.
func (r *Renderer) inputLine(text string, caretOn bool) string {
	line := "> " + text
	inner := r.theme.MaxWidth - 2*r.theme.Padding
	// Drop leading runes until the tail fits, so the caret stays visible.
	for font.MeasureString(r.face, line+"|").Ceil() > inner && len(line) > 3 {
		_, size := utf8.DecodeRuneInString(line[2:])
		line = "> " + line[2+size:]
	}
	if caretOn {
		line += "|"
	}
	return line
}

// RenderWithInput paints words above a line being typed, in one panel:
// a conversation, where the ghost's reply streams in while the next line
// is already open beneath it. The panel is the full width, like
// RenderInput's, so it can be redrawn in place until the words grow a
// line; the words keep their tail when they outgrow the height, as
// Render's do. With no words it is RenderInput.
func (r *Renderer) RenderWithInput(text, line string, caretOn bool) *sprite.Sprite {
	if strings.TrimSpace(text) == "" {
		return r.RenderInput(line, caretOn)
	}
	t := r.theme
	lh := r.lineHeight()
	gap := lh / 2
	words := r.wrap(strings.TrimSpace(text), t.MaxWidth-2*t.Padding)
	if t.MaxHeight > 0 {
		room := max((t.MaxHeight-2*t.Padding-gap)/lh-1, 1)
		if len(words) > room {
			words = append([]string{"…"}, words[len(words)-room+1:]...)
		}
	}

	img := image.NewNRGBA(image.Rect(0, 0, t.MaxWidth, lh*(len(words)+1)+gap+2*t.Padding))
	paintPanel(img, t)
	d := &font.Drawer{Dst: img, Src: image.NewUniform(t.Text), Face: r.face}
	y := t.Padding + r.face.Metrics().Ascent.Ceil()
	for _, l := range words {
		d.Dot = fixed.P(t.Padding, y)
		d.DrawString(l)
		y += lh
	}
	d.Dot = fixed.P(t.Padding, y+gap)
	d.DrawString(r.inputLine(line, caretOn))
	return &sprite.Sprite{Img: img}
}

// WaitFrames is how many distinct frames RenderWaiting cycles through.
const WaitFrames = 4

// RenderWaiting paints the "still working on it" indicator: three dots with
// the lit one travelling, and the ghost's own words for waiting beside them
// when its brain supplied any.
//
// The dots are the driver's, deliberately — every ghost gets a signal that
// it is thinking rather than broken, whether or not its author thought
// about slow backends. The text is the brain's, because "thinking…" in one
// ghost's voice is "hold on, hold on" in another's.
//
// The panel is a fixed size for a given text, so successive frames can be
// written into the buffer the compositor already holds (SpriteSurface.Redraw)
// instead of destroying and recreating a surface several times a second,
// which is visible as flicker. Same reasoning as the input line.
func (r *Renderer) RenderWaiting(text string, frame int) *sprite.Sprite {
	t := r.theme
	metrics := r.face.Metrics()
	lineHeight := (metrics.Ascent + metrics.Descent).Ceil() + 2

	// Dots sized off the font, so the indicator scales with everything else.
	dot := max(lineHeight/5, 2)
	gap := dot
	dotsW := 3*dot + 2*gap

	line := strings.TrimSpace(text)
	textW := 0
	if line != "" {
		// One line only: a waiting indicator that wraps to a paragraph is a
		// balloon, and the balloon is what we are waiting for.
		for font.MeasureString(r.face, line).Ceil() > t.MaxWidth-2*t.Padding-dotsW-gap*2 && len(line) > 1 {
			_, size := utf8.DecodeLastRuneInString(line)
			line = line[:len(line)-size]
		}
		textW = font.MeasureString(r.face, line).Ceil() + gap*2
	}

	w := t.Padding*2 + dotsW + textW
	h := t.Padding*2 + lineHeight
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	paintPanel(img, t)

	if line != "" {
		d := &font.Drawer{
			Dst:  img,
			Src:  image.NewUniform(t.Text),
			Face: r.face,
			Dot:  fixed.P(t.Padding, t.Padding+metrics.Ascent.Ceil()),
		}
		d.DrawString(line)
	}

	// The lit dot travels; frame 3 lights none, so the cycle reads as a
	// pulse rather than a wheel.
	cy := h / 2
	x := w - t.Padding - dotsW
	for i := 0; i < 3; i++ {
		c := t.Text
		if i != frame {
			c.A = c.A / 3
		}
		fillDot(img, x+dot/2, cy, dot/2, c)
		x += dot + gap
	}
	return &sprite.Sprite{Img: img}
}

// fillDot paints a filled circle, which is small enough that the naive loop
// is the whole of the algorithm.
func fillDot(img *image.NRGBA, cx, cy, rad int, c color.NRGBA) {
	if rad < 1 {
		rad = 1
	}
	for y := cy - rad; y <= cy+rad; y++ {
		for x := cx - rad; x <= cx+rad; x++ {
			dx, dy := x-cx, y-cy
			if dx*dx+dy*dy <= rad*rad {
				img.SetNRGBA(x, y, c)
			}
		}
	}
}

// RenderImage paints an image inside a balloon panel, scaled down to fit
// MaxWidth if needed (nearest-neighbor; panels are small).
func (r *Renderer) RenderImage(src image.Image) *sprite.Sprite {
	return r.RenderRecalledImage(src, "")
}

// RenderRecalledImage is RenderImage with a caption line beneath the
// picture — the footer a recalled line carries, for a recalled picture.
// An empty caption is no caption.
func (r *Renderer) RenderRecalledImage(src image.Image, caption string) *sprite.Sprite {
	t := r.theme
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	maxInner := t.MaxWidth - 2*t.Padding
	if w > maxInner {
		h = h * maxInner / w
		w = maxInner
	}
	inner, below := w, 0
	if caption != "" {
		below = r.lineHeight()
		if cw := font.MeasureString(r.face, caption).Ceil(); cw > inner {
			inner = cw
		}
	}
	panel := image.NewNRGBA(image.Rect(0, 0, inner+2*t.Padding, h+below+2*t.Padding))
	paintPanel(panel, t)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sx := b.Min.X + x*b.Dx()/w
			sy := b.Min.Y + y*b.Dy()/h
			panel.Set(t.Padding+x, t.Padding+y, src.At(sx, sy))
		}
	}
	if caption != "" {
		d := &font.Drawer{Dst: panel, Src: image.NewUniform(r.dim()), Face: r.face}
		d.Dot = fixed.P(t.Padding, t.Padding+h+r.face.Metrics().Ascent.Ceil()+2)
		d.DrawString(caption)
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
