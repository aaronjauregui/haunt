package shell

import (
	"image"
	"image/color"

	"github.com/aaronjauregui/haunt/internal/sprite"
)

// Builtin is the placeholder ghost, drawn rather than loaded — but drawn as
// a real layer stack with real poses and a blink, so a ghost with no art of
// its own can still change expression, and so the demo and the tests
// exercise the code path a hand-drawn shell does.
//
// Its face layers are cropped to the area they cover rather than being
// full-canvas images with transparent margins, which is how shell art
// should be authored: repaint damage is the union of the changed layers'
// bounds, so a blink drawn on a 68x19 patch repaints 68x19 pixels, and one
// drawn on a full-size transparent sheet repaints the whole ghost.
func Builtin(size int) *Shell {
	body := color.NRGBA{R: 240, G: 240, B: 255, A: 255}
	ink := color.NRGBA{R: 30, G: 30, B: 40, A: 255}

	r := float64(size) * 0.45
	cx, cy := float64(size)/2, float64(size)/2

	// Body: a round ghost, filling the canvas.
	bodyImg := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := float64(x)+0.5-cx, float64(y)+0.5-cy
			if dx*dx+dy*dy <= r*r {
				bodyImg.SetNRGBA(x, y, body)
			}
		}
	}

	// Eyes, on their own small patch.
	eyeR := r * 0.12
	eyeY := cy - r*0.2
	eyeXs := []float64{cx - r*0.35, cx + r*0.35}
	eyesAt := image.Pt(int(eyeXs[0]-eyeR)-1, int(eyeY-eyeR)-1)
	eyesW := int(eyeXs[1]+eyeR) - eyesAt.X + 2
	eyesH := int(eyeY+eyeR) - eyesAt.Y + 2

	discs := func(rad float64) *image.NRGBA {
		img := image.NewNRGBA(image.Rect(0, 0, eyesW, eyesH))
		for _, ex := range eyeXs {
			for y := 0; y < eyesH; y++ {
				for x := 0; x < eyesW; x++ {
					dx := float64(x+eyesAt.X) + 0.5 - ex
					dy := float64(y+eyesAt.Y) + 0.5 - eyeY
					if dx*dx+dy*dy <= rad*rad {
						img.SetNRGBA(x, y, ink)
					}
				}
			}
		}
		return img
	}
	open := discs(eyeR)
	happy := discs(eyeR * 0.6)

	closed := image.NewNRGBA(image.Rect(0, 0, eyesW, eyesH))
	for _, ex := range eyeXs {
		for x := int(ex - eyeR); x < int(ex+eyeR); x++ {
			for y := int(eyeY) - 1; y <= int(eyeY)+1; y++ {
				closed.SetNRGBA(x-eyesAt.X, y-eyesAt.Y, ink)
			}
		}
	}

	// Mouth, likewise: an arc on a small patch, bowing down for a smile and
	// up for a frown.
	mr := r * 0.3
	my := cy + r*0.25
	mouthAt := image.Pt(int(cx-mr)-1, int(my-mr*0.5)-1)
	mouthW := int(cx+mr) - mouthAt.X + 2
	mouthH := int(my+mr*0.5) - mouthAt.Y + 3

	arc := func(up bool) *image.NRGBA {
		img := image.NewNRGBA(image.Rect(0, 0, mouthW, mouthH))
		for x := -int(mr); x <= int(mr); x++ {
			fx := float64(x) / mr
			dy := (1 - fx*fx) * mr * 0.5
			if up {
				dy = -dy
			}
			py := int(my + dy)
			for t := 0; t < 2; t++ {
				img.SetNRGBA(int(cx)+x-mouthAt.X, py+t-mouthAt.Y, ink)
			}
		}
		return img
	}
	// An open mouth, for talking: a filled ellipse in the same patch.
	open_ := image.NewNRGBA(image.Rect(0, 0, mouthW, mouthH))
	for y := 0; y < mouthH; y++ {
		for x := 0; x < mouthW; x++ {
			dx := (float64(x+mouthAt.X) + 0.5 - cx) / (mr * 0.45)
			dy := (float64(y+mouthAt.Y) + 0.5 - my) / (mr * 0.4)
			if dx*dx+dy*dy <= 1 {
				open_.SetNRGBA(x, y, ink)
			}
		}
	}

	blank := image.NewNRGBA(image.Rect(0, 0, mouthW, mouthH))

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
		clips: []Clip{{
			// A blink: shut, then open again a moment later. Two keyframes
			// and two timers, which is the whole cost of looking alive.
			Name:    "blink",
			Trigger: TriggerSometimes,
			Keys: []Key{
				{At: 0, Set: map[string]string{"eyes": "closed"}},
				{At: 110, Set: map[string]string{"eyes": "open"}},
			},
		}, {
			// Talking: the mouth opens and closes for as long as words are
			// arriving. It overrides whatever mouth the pose chose and
			// hands it back when the line ends, which is what overrides
			// are for.
			Name:    "talk",
			Trigger: TriggerTalk,
			Keys: []Key{
				{At: 0, Set: map[string]string{"mouth": "open"}},
				{At: 130, Set: map[string]string{"mouth": "neutral"}},
				{At: 260, Set: map[string]string{"mouth": "open"}},
				{At: 400, Set: map[string]string{"mouth": "neutral"}},
			},
		}, {
			// Being poked: a quick squash and recover. Manual, because
			// only the brain knows it was poked.
			Name:    "poke",
			Trigger: TriggerManual,
			Keys: []Key{
				{At: 0, Move: map[string]Transform{"body": {Scale: 1}}},
				{At: 90, Ease: EaseSine, Move: map[string]Transform{
					"body": {Scale: 0.94, Offset: [2]float64{0, 4}},
				}},
				{At: 320, Ease: EaseSine, Move: map[string]Transform{
					"body": {Scale: 1},
				}},
			},
		}, {
			// A breath: two pixels, four seconds, eased so it drifts rather
			// than ticks. The face is parented to the body and rides along,
			// which is the whole point of the parent chain.
			Name:    "breathe",
			Trigger: TriggerAlways,
			Keys: []Key{
				{At: 0, Move: map[string]Transform{"body": {}}},
				{At: 2000, Ease: EaseSine, Move: map[string]Transform{
					"body": {Offset: [2]float64{0, 2}},
				}},
				{At: 4000, Ease: EaseSine, Move: map[string]Transform{
					"body": {Offset: [2]float64{0, 0}},
				}},
			},
		}},
	}
	s.layers = []*loaded{
		{
			Layer: Layer{
				Name: "body", Z: 0, Region: "body",
				// Centre pivot, so scaling squashes towards the middle
				// rather than out of the corner.
				Pivot: [2]int{size / 2, size / 2},
			},
			variants: map[string]*image.NRGBA{"": bodyImg},
		},
		{
			Layer: Layer{
				Name: "eyes", Z: 10, Default: "open", Region: "face",
				At: [2]int{eyesAt.X, eyesAt.Y}, Parent: "body",
			},
			variants: map[string]*image.NRGBA{"open": open, "closed": closed, "happy": happy},
			order:    1,
		},
		{
			Layer: Layer{
				Name: "mouth", Z: 11, Default: "neutral", Region: "face",
				At: [2]int{mouthAt.X, mouthAt.Y}, Parent: "body",
			},
			variants: map[string]*image.NRGBA{
				"neutral": blank, "smile": arc(false), "frown": arc(true), "open": open_,
			},
			order: 2,
		},
	}
	for _, l := range s.layers {
		s.current[l.Name] = l.Default
	}
	s.pose = "neutral"
	return s
}
