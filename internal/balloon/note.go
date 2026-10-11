package balloon

import (
	"image"
	"image/color"
	"math"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"github.com/aaronjauregui/haunt/internal/sprite"
)

// Desktop notes: a few words a ghost leaves on the screen to be found
// later, on a surface of their own (internal/ghost/desknote.go).
//
// A note is drawn by the same renderer as the balloon, so it comes out at
// the screen's real resolution like everything else the ghost says, but it
// does not look like a balloon. A balloon is speech and goes away; a note
// is paper and stays, and a person glancing at the desktop should be able
// to tell which is which without reading either. So the default is a light
// sheet with dark writing and nearly square corners, and a shell may ship
// its own paper instead ([shell] note).

const (
	// noteMaxWidth is the widest a note grows, in logical pixels: a slip
	// of paper rather than a page.
	noteMaxWidth = 220
	noteFontSize = 14
	notePadding  = 12
	noteRadius   = 4
	// notePictureGap separates a picture from the words under it.
	notePictureGap = 8
)

// NoteTheme is the default paper: a light sheet, dark writing.
func NoteTheme() Theme {
	return Theme{
		Background: color.NRGBA{R: 252, G: 246, B: 222, A: 250},
		Text:       color.NRGBA{R: 48, G: 44, B: 38, A: 255},
		Border:     color.NRGBA{R: 214, G: 200, B: 160, A: 255},
		MaxWidth:   noteMaxWidth,
		FontSize:   noteFontSize,
		Padding:    notePadding,
		Radius:     noteRadius,
	}
}

// noteFont is the parsed typeface, kept because a note's face is built
// per call at the renderer's scale and parsing it each time is waste.
var noteFont = sync.OnceValues(func() (*opentype.Font, error) { return opentype.Parse(goregular.TTF) })

// RenderNote paints a desktop note at the renderer's scale: text, with img
// above it at the note's inner width when there is one, on paper when the
// shell supplies it and on the default sheet otherwise.
//
// The default sheet is sized to the words, up to noteMaxWidth, and a note
// with a picture takes the full width so the picture is not a thumbnail.
// Supplied paper fixes the width at its own (it is the shell's art, drawn
// at the shell's size) and is stretched downwards when the words need more
// height than it has; it is scaled to the renderer's resolution so the
// writing on it is as sharp as a balloon's.
func (r *Renderer) RenderNote(text string, img image.Image, paper *sprite.Sprite) *sprite.Sprite {
	s := r.Scale()
	t := NoteTheme()
	px := func(v int) int { return int(math.Round(float64(v) * s)) }
	pad, maxW := px(t.Padding), px(t.MaxWidth)
	t.Padding, t.Radius = pad, px(t.Radius)

	face := r.face
	if ft, err := noteFont(); err == nil {
		if f, err := opentype.NewFace(ft, &opentype.FaceOptions{
			Size: noteFontSize * s, DPI: 72, Hinting: font.HintingFull,
		}); err == nil {
			face = f
		}
	}
	metrics := face.Metrics()
	lineH := (metrics.Ascent + metrics.Descent).Ceil() + 2

	var paperW, paperH int
	if paper != nil && paper.Img != nil {
		paperW, paperH = px(paper.Img.Bounds().Dx()), px(paper.Img.Bounds().Dy())
		maxW = max(paperW, 2*pad+1)
	}
	inner := maxW - 2*pad
	lines := wrapFace(face, strings.TrimSpace(text), inner)

	textW := 0
	for _, l := range lines {
		textW = max(textW, font.MeasureString(face, l).Ceil())
	}
	w := min(textW, inner) + 2*pad
	if img != nil || paperW > 0 {
		w = maxW
	}
	picW, picH := 0, 0
	if img != nil && img.Bounds().Dx() > 0 {
		picW = w - 2*pad
		picH = img.Bounds().Dy() * picW / img.Bounds().Dx()
	}
	h := 2*pad + lineH*len(lines)
	if picH > 0 {
		h += picH + px(notePictureGap)
	}
	h = max(h, paperH)

	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	if paperW > 0 {
		stretch(out, paper.Img)
	} else {
		paintPanel(out, t)
	}
	y := pad
	if picH > 0 {
		b := img.Bounds()
		for dy := 0; dy < picH; dy++ {
			for dx := 0; dx < picW; dx++ {
				out.Set(pad+dx, y+dy, img.At(b.Min.X+dx*b.Dx()/picW, b.Min.Y+dy*b.Dy()/picH))
			}
		}
		y += picH + px(notePictureGap)
	}
	d := &font.Drawer{Dst: out, Src: image.NewUniform(t.Text), Face: face}
	y += metrics.Ascent.Ceil()
	for _, l := range lines {
		d.Dot = fixed.P(pad, y)
		d.DrawString(l)
		y += lineH
	}
	return &sprite.Sprite{Img: out}
}

// stretch fills dst with src resampled to dst's size, nearest neighbour:
// paper is texture, and a soft edge on it costs more than it is worth.
func stretch(dst *image.NRGBA, src *image.NRGBA) {
	sb, db := src.Bounds(), dst.Bounds()
	if sb.Empty() {
		return
	}
	for y := 0; y < db.Dy(); y++ {
		sy := sb.Min.Y + y*sb.Dy()/db.Dy()
		for x := 0; x < db.Dx(); x++ {
			dst.SetNRGBA(x, y, src.NRGBAAt(sb.Min.X+x*sb.Dx()/db.Dx(), sy))
		}
	}
}

// wrapFace breaks text into lines no wider than maxWidth in face, on
// spaces, keeping explicit newlines. A word wider than the line on its own
// is left whole, as the balloon leaves it.
func wrapFace(face font.Face, text string, maxWidth int) []string {
	var lines []string
	for _, para := range strings.Split(text, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		cur := words[0]
		for _, word := range words[1:] {
			if cand := cur + " " + word; font.MeasureString(face, cand).Ceil() <= maxWidth {
				cur = cand
				continue
			}
			lines = append(lines, cur)
			cur = word
		}
		lines = append(lines, cur)
	}
	return lines
}
