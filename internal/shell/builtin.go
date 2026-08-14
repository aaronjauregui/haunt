package shell

import (
	"image"
	"image/color"

	"github.com/aaronjauregui/haunt/internal/sprite"
)

// Builtin is the placeholder ghost, drawn rather than loaded — but drawn as
// a real layer stack with real poses, so a ghost with no art of its own can
// still change expression, and so the demo and the tests exercise the same
// code path a hand-drawn shell does.
func Builtin(size int) *Shell {
	body := color.NRGBA{R: 240, G: 240, B: 255, A: 255}
	ink := color.NRGBA{R: 30, G: 30, B: 40, A: 255}

	r := float64(size) * 0.45
	cx, cy := float64(size)/2, float64(size)/2
	eyeR := r * 0.12
	eyeY := cy - r*0.2
	eyeXs := []float64{cx - r*0.35, cx + r*0.35}

	disc := func(w, h int, at []float64, rad float64, c color.NRGBA) *image.NRGBA {
		img := image.NewNRGBA(image.Rect(0, 0, w, h))
		for _, ax := range at {
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					dx, dy := float64(x)+0.5-ax, float64(y)+0.5-eyeY
					if dx*dx+dy*dy <= rad*rad {
						img.SetNRGBA(x, y, c)
					}
				}
			}
		}
		return img
	}

	// Body: a round ghost.
	bodyImg := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := float64(x)+0.5-cx, float64(y)+0.5-cy
			if dx*dx+dy*dy <= r*r {
				bodyImg.SetNRGBA(x, y, body)
			}
		}
	}

	// Eyes: open, closed (a squint line), and happy (small, high).
	open := disc(size, size, eyeXs, eyeR, ink)
	closed := image.NewNRGBA(image.Rect(0, 0, size, size))
	for _, ex := range eyeXs {
		for x := int(ex - eyeR); x < int(ex+eyeR); x++ {
			for y := int(eyeY) - 1; y <= int(eyeY)+1; y++ {
				closed.SetNRGBA(x, y, ink)
			}
		}
	}
	happy := disc(size, size, eyeXs, eyeR*0.6, ink)

	// Mouth: neutral (nothing), smile, frown — drawn as arcs.
	arc := func(up bool) *image.NRGBA {
		img := image.NewNRGBA(image.Rect(0, 0, size, size))
		mr := r * 0.3
		my := cy + r*0.25
		for x := -int(mr); x <= int(mr); x++ {
			fx := float64(x) / mr
			dy := (1 - fx*fx) * mr * 0.5
			if up {
				dy = -dy
			}
			py := int(my + dy)
			for t := 0; t < 2; t++ {
				img.SetNRGBA(int(cx)+x, py+t, ink)
			}
		}
		return img
	}

	s := &Shell{
		w: size, h: size,
		poses: map[string]map[string]string{
			"neutral":   {"eyes": "open", "mouth": "neutral"},
			"happy":     {"eyes": "happy", "mouth": "smile"},
			"sad":       {"eyes": "open", "mouth": "frown"},
			"surprised": {"eyes": "open", "mouth": "smile"},
			"asleep":    {"eyes": "closed", "mouth": "neutral"},
		},
		current: map[string]string{},
		cache:   map[string]*sprite.Sprite{},
	}
	blank := image.NewNRGBA(image.Rect(0, 0, size, size))
	s.layers = []*loaded{
		{
			Layer:    Layer{Name: "body", Z: 0, Region: "body"},
			variants: map[string]*image.NRGBA{"": bodyImg},
		},
		{
			Layer:    Layer{Name: "eyes", Z: 10, Default: "open", Region: "face"},
			variants: map[string]*image.NRGBA{"open": open, "closed": closed, "happy": happy},
			order:    1,
		},
		{
			Layer:    Layer{Name: "mouth", Z: 11, Default: "neutral", Region: "face"},
			variants: map[string]*image.NRGBA{"neutral": blank, "smile": arc(false), "frown": arc(true)},
			order:    2,
		},
	}
	for _, l := range s.layers {
		s.current[l.Name] = l.Default
	}
	s.pose = "neutral"
	return s
}
