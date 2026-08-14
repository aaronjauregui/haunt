// Package balloon renders dialogue balloons: word-wrapped text in a rounded
// panel, produced as an image for display on its own layer-shell surface.
// v1 is plain text on a fixed theme; markup and theme packages come later.
package balloon

import (
	"image"
	"image/color"
	"strings"

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
	FontSize float64
	Padding  int
	Radius   int
}

// DefaultTheme is a soft dark panel with light text.
func DefaultTheme() Theme {
	return Theme{
		Background: color.NRGBA{R: 34, G: 36, B: 54, A: 235},
		Text:       color.NRGBA{R: 235, G: 235, B: 245, A: 255},
		Border:     color.NRGBA{R: 120, G: 130, B: 180, A: 255},
		MaxWidth:   320,
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
