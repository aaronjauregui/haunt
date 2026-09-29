package seriko

import (
	"fmt"
	"image"
	"sort"

	"github.com/aaronjauregui/haunt/internal/shell"
)

// SERIKO animations, in haunt's vocabulary.
//
// A SERIKO animation is a list of patterns: draw surface 101 at (172,156)
// for 50ms, then 102, then 101, then clear. haunt says the same thing as a
// layer with named variants and a clip that swaps between them — which is
// the whole reason this conversion is mechanical rather than a rewrite.
//
// Two wrinkles have to be handled rather than assumed away:
//
//   - Patterns in one animation may sit at different offsets, while a haunt
//     layer has one. So every frame of an animation is padded into the
//     union of their rectangles, and the layer sits at that union's corner.
//     Constant-offset animations — nearly all of them — pad to nothing.
//   - A pattern drawn with `base` replaces the whole surface rather than
//     overlaying part of it. That is not a new layer, it is the body
//     changing, so those frames drive the body layer instead.

// convertAnimations turns every animation on every converted surface into
// clips, plus whatever layers those clips need.
func convertAnimations(sh *Shell, src Source, pics map[int]*image.NRGBA,
	base int, res *Result) ([]shell.Clip, []shell.Layer, []string) {

	var clips []shell.Clip
	var layers []shell.Layer
	var notes []string

	for _, id := range sortedSurfaceIDs(sh) {
		s := sh.Surfaces[id]
		if s == nil || len(s.Animations) == 0 {
			continue
		}
		if _, converted := pics[id]; !converted {
			continue
		}
		for _, aid := range sortedAnimIDs(s) {
			a := s.Animations[aid]
			if len(a.Patterns) == 0 {
				continue
			}
			clip, layer, note := convertAnimation(sh, src, pics, s, a, id == base, res)
			if note != "" {
				notes = append(notes, note)
			}
			if clip == nil {
				continue
			}
			clips = append(clips, *clip)
			if layer != nil {
				layers = append(layers, *layer)
			}
		}
	}
	return clips, layers, notes
}

// convertAnimation converts one animation. resting says this animation
// belongs to the surface the ghost wears at rest, which is what decides
// whether it may fire by itself.
func convertAnimation(sh *Shell, src Source, pics map[int]*image.NRGBA,
	s *Surface, a *Animation, resting bool, res *Result) (*shell.Clip, *shell.Layer, string) {

	name := fmt.Sprintf("s%d_anim%d", s.ID, a.ID)
	trigger, note := triggerFor(a, resting, name)
	if trigger == "" {
		return nil, nil, note
	}

	// Frames drawn with `base` swap the whole body; everything else is an
	// overlay of its own.
	overlay := false
	for _, p := range a.Patterns {
		if !p.Clearing() && p.Method != "base" {
			overlay = true
			break
		}
	}

	if !overlay {
		clip := baseClip(name, trigger, a, pics, s.ID)
		if clip == nil {
			return nil, nil, fmt.Sprintf("%s: none of its frames could be read", name)
		}
		return clip, nil, note
	}

	// The union of every frame's rectangle, so one layer offset serves them
	// all.
	union := image.Rectangle{}
	frames := map[int]*image.NRGBA{}
	for _, p := range a.Patterns {
		if p.Clearing() {
			continue
		}
		img, err := frameImage(sh, src, pics, p.Surface)
		if err != nil {
			continue
		}
		frames[p.Surface] = img
		r := image.Rect(p.X, p.Y, p.X+img.Bounds().Dx(), p.Y+img.Bounds().Dy())
		if union.Empty() {
			union = r
		} else {
			union = union.Union(r)
		}
	}
	if union.Empty() {
		return nil, nil, fmt.Sprintf("%s: none of its frames could be read", name)
	}

	layer := shell.Layer{
		Name:     name,
		Variants: map[string]string{},
		// Nothing, at rest: an animation layer is invisible until its clip
		// runs, or every one of them would be showing its first frame the
		// moment the ghost appeared.
		Default: "none",
		At:      [2]int{union.Min.X, union.Min.Y},
		Z:       10 + a.ID,
	}
	// "nothing" is a real one-pixel transparent file rather than an absent
	// one: a layer has to be able to *wear* it, and only a masked layer
	// has "no art" as a variant of its own. These layers have no mask to
	// be held to, since SERIKO's animations go wherever their frames put
	// them.
	nonePath := "shell/" + name + "_none.png"
	noneData, err := encodePNG(blank(1, 1))
	if err != nil {
		return nil, nil, fmt.Sprintf("%s: %v", name, err)
	}
	res.Files[nonePath] = noneData
	layer.Variants["none"] = nonePath

	var keys []shell.Key
	at := 0
	written := map[string]bool{}
	for _, p := range a.Patterns {
		// The wait comes *first*. SERIKO's wait is "how long until this
		// frame is drawn", not "how long it stays" — so a pattern's time is
		// the running total including its own wait, and the frame before it
		// is what holds for that long. Reading it as a duration shifts every
		// frame early and truncates the last one's hold, which on a
		// seven-frame `runonce` is the difference between a gesture and a
		// twitch.
		at += p.WaitMS
		set := map[string]string{}
		if p.Clearing() {
			set[name] = "none"
		} else {
			img, ok := frames[p.Surface]
			if !ok {
				continue
			}
			v := variantName(p.Surface)
			if !written[v] {
				written[v] = true
				// Padded into the union so every variant shares one offset,
				// then trimmed back by the shell loader's own rules when it
				// draws — the padding is only here to make the offsets
				// agree.
				padded := blank(union.Dx(), union.Dy())
				pasteAt(padded, img, p.X-union.Min.X, p.Y-union.Min.Y)
				data, err := encodePNG(padded)
				if err != nil {
					return nil, nil, fmt.Sprintf("%s: %v", name, err)
				}
				path := fmt.Sprintf("shell/%s_%s.png", name, v)
				res.Files[path] = data
				layer.Variants[v] = path
			}
			set[name] = v
		}
		keys = append(keys, shell.Key{At: at, Set: set})
	}
	if len(keys) == 0 {
		return nil, nil, fmt.Sprintf("%s: no usable frames", name)
	}
	// A clip that never returns to nothing would leave its last frame on
	// screen for good, which for a blink means permanently shut eyes.
	if last := a.Patterns[len(a.Patterns)-1]; !last.Clearing() {
		keys = append(keys, shell.Key{At: at, Set: map[string]string{name: "none"}})
	}

	return &shell.Clip{Name: name, Trigger: trigger, Keys: keys}, &layer, note
}

// baseClip builds a clip whose frames replace the body outright.
func baseClip(name, trigger string, a *Animation, pics map[int]*image.NRGBA, own int) *shell.Clip {
	var keys []shell.Key
	at := 0
	any := false
	for _, p := range a.Patterns {
		// The wait leads the frame, as above.
		at += p.WaitMS
		v := variantName(own)
		if !p.Clearing() {
			if _, ok := pics[p.Surface]; !ok {
				continue
			}
			v = variantName(p.Surface)
			any = true
		}
		keys = append(keys, shell.Key{At: at, Set: map[string]string{bodyLayer: v}})
	}
	if !any {
		return nil
	}
	// Back to the surface it started from, or the ghost keeps whichever
	// frame the clip ended on.
	keys = append(keys, shell.Key{At: at, Set: map[string]string{bodyLayer: variantName(own)}})
	return &shell.Clip{Name: name, Trigger: trigger, Keys: keys}
}

// frameImage is the art for one animation frame.
func frameImage(sh *Shell, src Source, pics map[int]*image.NRGBA, id int) (*image.NRGBA, error) {
	if img, ok := pics[id]; ok {
		return img, nil
	}
	return surfaceImage(sh, src, id)
}

// pasteAt copies src into dst at an offset, clipping at the edges.
func pasteAt(dst, src *image.NRGBA, x, y int) {
	sb := src.Bounds()
	for sy := 0; sy < sb.Dy(); sy++ {
		dy := y + sy
		if dy < 0 || dy >= dst.Bounds().Dy() {
			continue
		}
		for sx := 0; sx < sb.Dx(); sx++ {
			dx := x + sx
			if dx < 0 || dx >= dst.Bounds().Dx() {
				continue
			}
			si := src.PixOffset(sx, sy)
			di := dst.PixOffset(dx, dy)
			copy(dst.Pix[di:di+4], src.Pix[si:si+4])
		}
	}
}

// triggerFor maps a SERIKO interval onto one of haunt's triggers.
//
// Only the resting surface's animations may fire by themselves. A blink
// drawn for surface 0's eyes is positioned for surface 0's face, and firing
// it while the ghost wears another surface would land it somewhere on the
// character's ear — a bug that looks like bad art. The rest become manual
// clips a brain can ask for by name, which is exactly what a brain that
// knows which pose it put on is in a position to do.
func triggerFor(a *Animation, resting bool, name string) (string, string) {
	if !resting {
		return shell.TriggerManual, fmt.Sprintf(
			"%s belongs to a surface the ghost does not rest on, so it is a manual clip: "+
				"its frames are placed for that surface's art", name)
	}
	switch a.Interval {
	case "always":
		return shell.TriggerAlways, ""
	case "runonce":
		return shell.TriggerRunOnce, ""
	case "talk", "starttalk", "endtalk":
		return shell.TriggerTalk, ""
	case "never", "":
		return shell.TriggerManual, ""
	case "sometimes", "rarely", "random", "periodic":
		return byPeriod(a, name)
	case "bind":
		// Dressup: a static overlay chosen by the user, not an animation.
		// haunt has no wardrobe, so the honest answer is a manual clip and
		// a note rather than a costume that puts itself on.
		return shell.TriggerManual, fmt.Sprintf(
			"%s is a dressup (bind), which this format has no equivalent for; "+
				"it is a manual clip, so a brain can put the outfit on", name)
	}
	return shell.TriggerManual, fmt.Sprintf(
		"%s has interval %q, which has no equivalent here; it became a manual clip",
		name, a.Interval)
}

// periodMS is how often a SERIKO interval actually fires, in milliseconds,
// or zero for the triggers that are not on a clock.
func periodMS(a *Animation) int {
	switch a.Interval {
	case "sometimes":
		return 2000 // one-in-two per second
	case "rarely":
		return 4000 // one-in-four per second
	case "random", "periodic":
		if a.Every > 0 {
			return a.Every * 1000
		}
		return 4000
	}
	return 0
}

// byPeriod picks a haunt trigger from how often the SERIKO one actually
// fires, rather than from what it is called.
//
// The names are a trap. Both formats have `sometimes` and `rarely` and they
// mean nearly opposite things: SERIKO's are probabilities *per second* —
// `sometimes` is one-in-two, so about every two seconds, and `rarely` is
// one-in-four, about every four — while haunt's are interval ranges of
// 3-12s and 45-150s. Matching the words maps a four-second blink onto a
// ninety-second one and the ghost stops looking alive.
func byPeriod(a *Animation, name string) (string, string) {
	// The crossover sits between haunt's two ranges and nearer the fast
	// one, because that is where nearly every shell animation lives:
	// blinks, breathing, hair. Anything slower than half a minute was meant
	// to be occasional and can have the slow trigger.
	seconds := float64(periodMS(a)) / 1000
	if seconds <= 25 {
		return shell.TriggerSometimes, ""
	}
	return shell.TriggerRarely, ""
}

func sortedAnimIDs(s *Surface) []int {
	out := make([]int, 0, len(s.Animations))
	for id := range s.Animations {
		out = append(out, id)
	}
	sort.Ints(out)
	return out
}
