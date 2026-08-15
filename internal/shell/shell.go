// Package shell assembles a ghost's visual form from layers.
//
// A shell is a stack of named layers, each of which may have several
// variants — eyes open, eyes closed, eyes happy — and a pose is a choice of
// variant per layer. That is the whole model, and it is deliberately the
// same one the ukagaka ecosystem arrived at (SERIKO's elements and binds):
// layered art composes expressions and dress-up without redrawing a body
// for every combination.
//
// The format nests, so complexity is only paid for when used:
//
//	one PNG                → one layer, one pose, no animation
//	+ variants             → expressions
//	+ clips (later)        → animation by swapping variants on a clock
//	+ transforms (later)   → interpolated motion of layers
//
// Layers carry `parent` and `pivot` from the start even though nothing
// transforms them yet: a rig where rotating an upper arm carries the
// forearm cannot be retrofitted onto shells authored without it.
package shell

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aaronjauregui/haunt/internal/sprite"
)

// Layer is one piece of art in the stack.
type Layer struct {
	// Name identifies the layer, and is the slot a pose selects into.
	Name string `toml:"name"`
	// Image is the art for a layer with only one appearance. Mutually
	// exclusive with Variants.
	Image string `toml:"image"`
	// Variants are named alternatives — the point of the whole format.
	Variants map[string]string `toml:"variants"`
	// Default is the variant shown when no pose selects one.
	Default string `toml:"default"`
	// Z orders the stack; higher draws later. Ties break on declaration
	// order.
	Z int `toml:"z"`
	// At offsets the layer within the shell canvas.
	At [2]int `toml:"at"`
	// Pivot is the point transforms rotate and scale about, relative to
	// the layer's own origin. Unused until animation lands.
	Pivot [2]int `toml:"pivot"`
	// Parent nests this layer's transform inside another's, so a rotating
	// arm carries its hand. Unused until animation lands, validated now.
	Parent string `toml:"parent"`
	// Region names the part of the ghost this layer represents (head,
	// face, hand). Clicks report the topmost region they land on, which is
	// how a brain can tell being patted from being poked.
	Region string `toml:"region"`
}

// Manifest is the [shell] section: either a single sprite or a layer stack.
type Manifest struct {
	// Sprite is the whole-shell image, the simplest possible shell.
	Sprite string `toml:"sprite"`
	// Layers, when present, replace Sprite.
	Layers []Layer `toml:"layer"`
	// Poses map a name to the variant chosen per layer. Layers not
	// mentioned keep their default.
	Poses map[string]map[string]string `toml:"pose"`
	// Default names the pose worn at boot; empty uses every layer's own
	// default.
	Default string `toml:"default_pose"`
	// Clips are the shell's animations.
	Clips []Clip `toml:"clip"`
}

// loaded is a layer with its art decoded.
type loaded struct {
	Layer
	// variants holds every decoded appearance, keyed by variant name. A
	// single-image layer stores it under "".
	variants map[string]*image.NRGBA
	order    int
}

// Shell is a loaded, composable ghost body.
type Shell struct {
	w, h    int
	layers  []*loaded
	poses   map[string]map[string]string
	pose    string
	current map[string]string // layer name -> variant, from the pose
	// overrides is what a running clip is showing right now, on top of the
	// pose; moves are where clips have placed layers.
	overrides map[string]string
	moves     map[string]Transform
	clips     []Clip

	// cache holds composited poses. A ghost that is not changing
	// expression composites nothing.
	cache map[string]*sprite.Sprite
}

// Load decodes a shell manifest relative to dir.
func Load(dir string, m Manifest) (*Shell, error) {
	if len(m.Layers) == 0 {
		return loadSingle(dir, m)
	}
	s := &Shell{
		poses:   m.Poses,
		current: map[string]string{},
		cache:   map[string]*sprite.Sprite{},
	}
	names := map[string]bool{}
	for i, l := range m.Layers {
		if l.Name == "" {
			return nil, fmt.Errorf("shell layer %d has no name", i)
		}
		if names[l.Name] {
			return nil, fmt.Errorf("shell layer %q declared twice", l.Name)
		}
		names[l.Name] = true

		if l.Image == "" && len(l.Variants) == 0 {
			return nil, fmt.Errorf("shell layer %q has no image or variants", l.Name)
		}
		if l.Image != "" && len(l.Variants) > 0 {
			return nil, fmt.Errorf("shell layer %q has both image and variants", l.Name)
		}

		ld := &loaded{Layer: l, variants: map[string]*image.NRGBA{}, order: i}
		if l.Image != "" {
			img, err := loadImage(dir, l.Image)
			if err != nil {
				return nil, fmt.Errorf("shell layer %q: %w", l.Name, err)
			}
			ld.variants[""] = img
			ld.Default = ""
		} else {
			for name, path := range l.Variants {
				img, err := loadImage(dir, path)
				if err != nil {
					return nil, fmt.Errorf("shell layer %q variant %q: %w", l.Name, name, err)
				}
				ld.variants[name] = img
			}
			if ld.Default == "" {
				return nil, fmt.Errorf("shell layer %q has variants but no default", l.Name)
			}
			if _, ok := ld.variants[ld.Default]; !ok {
				return nil, fmt.Errorf("shell layer %q defaults to unknown variant %q", l.Name, ld.Default)
			}
		}
		s.current[l.Name] = ld.Default
		s.layers = append(s.layers, ld)
	}

	if err := s.validateParents(names); err != nil {
		return nil, err
	}
	for pose, sel := range m.Poses {
		for layer, variant := range sel {
			ld := s.layer(layer)
			if ld == nil {
				return nil, fmt.Errorf("pose %q selects unknown layer %q", pose, layer)
			}
			if _, ok := ld.variants[variant]; !ok {
				return nil, fmt.Errorf("pose %q selects unknown variant %q of layer %q", pose, variant, layer)
			}
		}
	}

	if err := s.loadClips(m.Clips); err != nil {
		return nil, err
	}

	sort.SliceStable(s.layers, func(i, j int) bool {
		if s.layers[i].Z != s.layers[j].Z {
			return s.layers[i].Z < s.layers[j].Z
		}
		return s.layers[i].order < s.layers[j].order
	})
	s.w, s.h = s.canvas()

	if m.Default != "" {
		if !s.SetPose(m.Default) {
			return nil, fmt.Errorf("default_pose %q is not a declared pose", m.Default)
		}
	}
	return s, nil
}

// loadSingle wraps a one-image shell (or the built-in placeholder) as a
// one-layer stack, so everything downstream sees the same thing.
func loadSingle(dir string, m Manifest) (*Shell, error) {
	var img *image.NRGBA
	if m.Sprite == "" {
		img = Builtin(160).layers[0].variants[""]
	} else {
		var err error
		if img, err = loadImage(dir, m.Sprite); err != nil {
			return nil, err
		}
	}
	b := img.Bounds()
	return &Shell{
		w: b.Dx(), h: b.Dy(),
		layers: []*loaded{{
			Layer:    Layer{Name: "body"},
			variants: map[string]*image.NRGBA{"": img},
		}},
		current: map[string]string{"body": ""},
		cache:   map[string]*sprite.Sprite{},
	}, nil
}

func loadImage(dir, path string) (*image.NRGBA, error) {
	sp, err := sprite.Load(filepath.Join(dir, path))
	if err != nil {
		return nil, err
	}
	return sp.Img, nil
}

// validateParents rejects unknown parents and cycles, so a rig cannot hang
// the compositor once transforms arrive.
func (s *Shell) validateParents(names map[string]bool) error {
	parent := map[string]string{}
	for _, l := range s.layers {
		if l.Parent == "" {
			continue
		}
		if !names[l.Parent] {
			return fmt.Errorf("shell layer %q has unknown parent %q", l.Name, l.Parent)
		}
		if l.Parent == l.Name {
			return fmt.Errorf("shell layer %q is its own parent", l.Name)
		}
		parent[l.Name] = l.Parent
	}
	for start := range parent {
		seen := map[string]bool{start: true}
		for at := parent[start]; at != ""; at = parent[at] {
			if seen[at] {
				return fmt.Errorf("shell layers form a parent cycle at %q", at)
			}
			seen[at] = true
		}
	}
	return nil
}

// canvas is the union of every layer's placed bounds.
func (s *Shell) canvas() (w, h int) {
	for _, l := range s.layers {
		for _, img := range l.variants {
			b := img.Bounds()
			if x := l.At[0] + b.Dx(); x > w {
				w = x
			}
			if y := l.At[1] + b.Dy(); y > h {
				h = y
			}
		}
	}
	return w, h
}

func (s *Shell) layer(name string) *loaded {
	for _, l := range s.layers {
		if l.Name == name {
			return l
		}
	}
	return nil
}

// Size is the shell canvas in pixels.
func (s *Shell) Size() (w, h int) { return s.w, s.h }

// Poses lists the declared pose names, which is the vocabulary expression
// inference is allowed to choose from.
func (s *Shell) Poses() []string {
	out := make([]string, 0, len(s.poses))
	for name := range s.poses {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Pose is the pose currently worn, empty if none was ever set.
func (s *Shell) Pose() string { return s.pose }

// SetPose selects a declared pose, reporting whether it exists. Layers the
// pose does not mention return to their default, so poses describe a whole
// face rather than a diff from whatever came before.
func (s *Shell) SetPose(name string) bool {
	sel, ok := s.poses[name]
	if !ok {
		return false
	}
	for _, l := range s.layers {
		if v, set := sel[l.Name]; set {
			s.current[l.Name] = v
		} else {
			s.current[l.Name] = l.Default
		}
	}
	s.pose = name
	return true
}

// loadClips validates animations against the layers they drive.
func (s *Shell) loadClips(clips []Clip) error {
	seen := map[string]bool{}
	for _, c := range clips {
		if c.Name == "" {
			return fmt.Errorf("shell clip has no name")
		}
		if seen[c.Name] {
			return fmt.Errorf("shell clip %q declared twice", c.Name)
		}
		seen[c.Name] = true
		switch c.Trigger {
		case TriggerAlways, TriggerSometimes, TriggerRarely, TriggerRunOnce, TriggerManual, "":
		default:
			return fmt.Errorf("shell clip %q has unknown trigger %q", c.Name, c.Trigger)
		}
		if len(c.Keys) == 0 {
			return fmt.Errorf("shell clip %q has no keys", c.Name)
		}
		last := -1
		for i, k := range c.Keys {
			if k.At < 0 {
				return fmt.Errorf("shell clip %q key %d is at a negative time", c.Name, i)
			}
			if k.At < last {
				return fmt.Errorf("shell clip %q keys are out of time order", c.Name)
			}
			last = k.At
			for layer, variant := range k.Set {
				ld := s.layer(layer)
				if ld == nil {
					return fmt.Errorf("shell clip %q drives unknown layer %q", c.Name, layer)
				}
				if _, ok := ld.variants[variant]; !ok {
					return fmt.Errorf("shell clip %q selects unknown variant %q of layer %q", c.Name, variant, layer)
				}
			}
		}
		if c.Trigger == "" {
			c.Trigger = TriggerManual
		}
		s.clips = append(s.clips, c)
	}
	return nil
}

// selection is the variant showing on each layer: the pose, with any
// running clip's frame on top.
func (s *Shell) selection(name string) string {
	if v, ok := s.overrides[name]; ok {
		return v
	}
	return s.current[name]
}

// key identifies the current appearance for caching: which variants are
// showing, and where they have been moved to. Interpolated motion makes a
// fresh key every frame, so the cache stops helping mid-animation — which
// is correct, since every frame genuinely differs.
func (s *Shell) key() string {
	parts := make([]string, 0, len(s.layers))
	for _, l := range s.layers {
		part := l.Name + "=" + s.selection(l.Name)
		if t := s.resolve(l.Name); t.moved() {
			opacity := 1.0
			if t.Opacity != nil {
				opacity = *t.Opacity
			}
			part += fmt.Sprintf("@%.2f,%.2f,%.2f,%.3f,%.3f",
				t.Offset[0], t.Offset[1], t.Rotate, t.Scale, opacity)
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ",")
}

// Sprite composites the current pose. Repeated calls for an unchanged pose
// return the cached result, so a still ghost costs nothing.
func (s *Shell) Sprite() *sprite.Sprite {
	k := s.key()
	if sp, ok := s.cache[k]; ok {
		return sp
	}
	img := image.NewNRGBA(image.Rect(0, 0, s.w, s.h))
	for _, l := range s.layers {
		art := l.variants[s.selection(l.Name)]
		if art == nil {
			continue
		}
		b := art.Bounds()
		t := s.resolve(l.Name)
		if !t.moved() {
			at := image.Rect(l.At[0], l.At[1], l.At[0]+b.Dx(), l.At[1]+b.Dy())
			draw.Draw(img, at, art, b.Min, draw.Over)
			continue
		}
		drawTransformed(img, art, l.Layer, t)
	}
	sp := &sprite.Sprite{Img: img}
	s.cache[k] = sp
	return sp
}

// RegionAt names the part of the ghost at a point in shell coordinates:
// the topmost layer that both declares a region and is opaque there. Empty
// when the point hits no named part.
func (s *Shell) RegionAt(x, y int) string {
	const alphaThreshold = 16
	for i := len(s.layers) - 1; i >= 0; i-- {
		l := s.layers[i]
		if l.Region == "" {
			continue
		}
		art := l.variants[s.selection(l.Name)]
		if art == nil {
			continue
		}
		p := image.Pt(x-l.At[0], y-l.At[1])
		if !p.In(art.Bounds()) {
			continue
		}
		if _, _, _, a := art.At(p.X, p.Y).RGBA(); a>>8 >= alphaThreshold {
			return l.Region
		}
	}
	return ""
}

// Clip is an animation: a sequence of keyframes that swap layer variants on
// a clock. This is SERIKO's pattern animation, in the vocabulary the rest of
// the format already uses — a blink is two keyframes 90ms apart selecting
// the closed and open eyes.
//
// Keyframes carry only `set` for now. Eased transforms (`move`) belong on
// the same keyframe when they land, which is why a keyframe is a struct
// rather than a bare variant selection.
type Clip struct {
	Name string `toml:"name"`
	// Trigger says who starts it:
	//   always    — restart as soon as it ends
	//   sometimes — at random intervals of a few seconds
	//   rarely    — at random intervals of a minute or so
	//   runonce   — once, at boot
	//   manual    — only when the brain asks
	Trigger string `toml:"trigger"`
	// Keys are the frames, in time order from the clip's start.
	Keys []Key `toml:"keys"`
}

// Key is one frame of a clip.
type Key struct {
	// At is milliseconds from the start of the clip.
	At int `toml:"at"`
	// Set chooses a variant per layer, exactly as a pose does. Layers not
	// mentioned keep whatever the pose selected, so a blink can leave the
	// mouth alone. Stepped: you cannot tween a change of drawing.
	Set map[string]string `toml:"set"`
	// Move places layers. Interpolated from the previous keyframe's value,
	// which is what a frame-swap format cannot do: a breath or a head tilt
	// becomes a curve instead of eight drawings.
	Move map[string]Transform `toml:"move"`
	// Ease shapes that interpolation on the way *to* this keyframe:
	// "linear" (default), "sine" (smooth in and out), or "step" (hold the
	// previous value until this keyframe arrives).
	Ease string `toml:"ease"`
}

// Transform places a layer relative to where its art sits. The zero value
// is "unmoved", so a keyframe naming only Offset leaves rotation and scale
// alone.
type Transform struct {
	// Offset shifts the layer, in shell pixels.
	Offset [2]float64 `toml:"offset"`
	// Rotate turns it about its pivot, in degrees, clockwise.
	Rotate float64 `toml:"rotate"`
	// Scale multiplies its size about its pivot. Zero means 1.
	Scale float64 `toml:"scale"`
	// Opacity fades it, 0 to 1. A nil pointer means fully opaque, so that
	// "unset" and "invisible" are not the same value.
	Opacity *float64 `toml:"opacity"`
}

// Easing names.
const (
	EaseLinear = "linear"
	EaseSine   = "sine"
	EaseStep   = "step"
)

// identity is the transform of a layer nothing has touched.
var identity = Transform{Scale: 1}

// normalized fills in the zero values that mean "unchanged".
func (t Transform) normalized() Transform {
	if t.Scale == 0 {
		t.Scale = 1
	}
	return t
}

// lerp interpolates between two transforms. frac is already eased.
func lerp(a, b Transform, frac float64) Transform {
	a, b = a.normalized(), b.normalized()
	out := Transform{
		Offset: [2]float64{
			a.Offset[0] + (b.Offset[0]-a.Offset[0])*frac,
			a.Offset[1] + (b.Offset[1]-a.Offset[1])*frac,
		},
		Rotate: a.Rotate + (b.Rotate-a.Rotate)*frac,
		Scale:  a.Scale + (b.Scale-a.Scale)*frac,
	}
	av, bv := 1.0, 1.0
	if a.Opacity != nil {
		av = *a.Opacity
	}
	if b.Opacity != nil {
		bv = *b.Opacity
	}
	if a.Opacity != nil || b.Opacity != nil {
		v := av + (bv-av)*frac
		out.Opacity = &v
	}
	return out
}

// ease shapes a 0..1 fraction.
func ease(name string, f float64) float64 {
	switch name {
	case EaseStep:
		return 0
	case EaseSine:
		// Smooth in and out; the motion a living thing makes.
		return 0.5 - 0.5*math.Cos(f*math.Pi)
	default:
		return f
	}
}

// Trigger values.
const (
	TriggerAlways    = "always"
	TriggerSometimes = "sometimes"
	TriggerRarely    = "rarely"
	TriggerRunOnce   = "runonce"
	TriggerManual    = "manual"
)

// Clips lists the shell's animations.
func (s *Shell) Clips() []Clip { return s.clips }

// Clip finds an animation by name.
func (s *Shell) Clip(name string) (Clip, bool) {
	for _, c := range s.clips {
		if c.Name == name {
			return c, true
		}
	}
	return Clip{}, false
}

// SetOverrides layers a clip's current frame over the pose. Overrides are
// transient: the pose is what the ghost looks like, a clip is what it is
// doing at this instant.
func (s *Shell) SetOverrides(set map[string]string) {
	s.overrides = set
}

// SetMoves places layers for the current instant of an animation.
func (s *Shell) SetMoves(move map[string]Transform) {
	s.moves = move
}

// resolve composes a layer's transform with its parents', so rotating an
// upper arm carries the forearm and the hand. Cycles are impossible: the
// loader rejects them.
func (s *Shell) resolve(name string) Transform {
	t := s.moves[name]
	t = t.normalized()
	l := s.layer(name)
	if l == nil || l.Parent == "" {
		return t
	}
	p := s.resolve(l.Parent)
	// The parent's rotation and scale apply about the parent's pivot, and
	// carry the child's own offset with them.
	rad := p.Rotate * math.Pi / 180
	cos, sin := math.Cos(rad), math.Sin(rad)
	ox, oy := t.Offset[0]*p.Scale, t.Offset[1]*p.Scale
	out := Transform{
		Offset: [2]float64{
			p.Offset[0] + ox*cos - oy*sin,
			p.Offset[1] + ox*sin + oy*cos,
		},
		Rotate: p.Rotate + t.Rotate,
		Scale:  p.Scale * t.Scale,
	}
	switch {
	case t.Opacity != nil && p.Opacity != nil:
		v := *t.Opacity * *p.Opacity
		out.Opacity = &v
	case t.Opacity != nil:
		out.Opacity = t.Opacity
	case p.Opacity != nil:
		out.Opacity = p.Opacity
	}
	return out
}

// moved reports whether a transform actually does anything, so an
// untransformed shell composites by plain blitting as before.
func (t Transform) moved() bool {
	t = t.normalized()
	return t.Offset != [2]float64{} || t.Rotate != 0 || t.Scale != 1 ||
		(t.Opacity != nil && *t.Opacity < 1)
}

// Duration is how long a clip runs, which is the time of its last keyframe.
func (c Clip) Duration() int {
	if len(c.Keys) == 0 {
		return 0
	}
	return c.Keys[len(c.Keys)-1].At
}

// Frame returns the variant selections in force at elapsed milliseconds,
// and how long until the next keyframe. done reports that the clip has
// finished, at which point the caller loops or clears it.
//
// This is deliberately a pure function of elapsed time: the driver schedules
// a wake-up per keyframe rather than running a frame loop, so a blink costs
// two timers rather than thirty repaints a second.
func (c Clip) Frame(elapsed int) (set map[string]string, nextIn int, done bool) {
	set, _, nextIn, done = c.FrameAt(elapsed)
	return set, nextIn, done
}

// FrameAt is Frame plus the interpolated transforms, and reports whether
// anything is mid-interpolation — which is the one case the driver has to
// sample rather than sleep through.
func (c Clip) FrameAt(elapsed int) (set map[string]string, move map[string]Transform, nextIn int, done bool) {
	if len(c.Keys) == 0 {
		return nil, nil, 0, true
	}
	set = map[string]string{}
	move = map[string]Transform{}
	// from holds the transform each layer is interpolating away from.
	from := map[string]Transform{}

	for i, k := range c.Keys {
		if k.At > elapsed {
			// Mid-segment: blend towards this keyframe's transforms.
			prevAt := 0
			if i > 0 {
				prevAt = c.Keys[i-1].At
			}
			span := k.At - prevAt
			frac := 1.0
			if span > 0 {
				frac = float64(elapsed-prevAt) / float64(span)
			}
			frac = ease(k.Ease, frac)
			for layer, to := range k.Move {
				start, ok := from[layer]
				if !ok {
					start = identity
				}
				move[layer] = lerp(start, to, frac)
			}
			return set, move, k.At - elapsed, false
		}
		// Keyframes accumulate: a later key that mentions only the mouth
		// leaves an earlier key's eyes in place.
		for layer, variant := range k.Set {
			set[layer] = variant
		}
		for layer, t := range k.Move {
			move[layer] = t.normalized()
			from[layer] = t
		}
	}
	return set, move, 0, true
}

// Animates reports whether the clip moves anything, as opposed to only
// swapping variants. Only moving clips need sampling between keyframes.
func (c Clip) Animates() bool {
	for _, k := range c.Keys {
		if len(k.Move) > 0 {
			return true
		}
	}
	return false
}

// State is what the shell looks like at an instant: which variant each
// layer shows and where it has been moved to. Callers keep one to ask what
// has changed since they last painted.
type State struct {
	selection map[string]string
	moves     map[string]Transform
}

// State snapshots the current appearance.
func (s *Shell) State() State {
	st := State{
		selection: make(map[string]string, len(s.layers)),
		moves:     make(map[string]Transform, len(s.layers)),
	}
	for _, l := range s.layers {
		st.selection[l.Name] = s.selection(l.Name)
		st.moves[l.Name] = s.resolve(l.Name)
	}
	return st
}

// Damage is the region that differs from a previous state: the union of the
// bounds of every layer whose variant or placement changed, in shell
// coordinates. A zero previous state means everything; an empty rectangle
// means nothing changed and there is nothing to repaint.
//
// Both the old and new appearance count, since a layer that moved has to be
// erased from where it was as well as drawn where it is now.
func (s *Shell) Damage(prev State) image.Rectangle {
	whole := image.Rect(0, 0, s.w, s.h)
	if prev.selection == nil {
		return whole
	}
	var out image.Rectangle
	for _, l := range s.layers {
		now, nowT := s.selection(l.Name), s.resolve(l.Name)
		was, seen := prev.selection[l.Name]
		wasT := prev.moves[l.Name]
		if seen && was == now && wasT == nowT {
			continue
		}
		for _, st := range []struct {
			variant string
			t       Transform
		}{{now, nowT}, {was, wasT}} {
			art, ok := l.variants[st.variant]
			if !ok {
				continue
			}
			out = out.Union(layerBounds(art.Bounds(), l.Layer, st.t))
		}
	}
	return out.Intersect(whole)
}

// layerBounds is where a layer's art lands in shell coordinates under a
// transform.
func layerBounds(b image.Rectangle, l Layer, t Transform) image.Rectangle {
	t = t.normalized()
	if !t.moved() {
		return b.Add(image.Pt(l.At[0], l.At[1]))
	}
	rad := t.Rotate * math.Pi / 180
	cos, sin := math.Cos(rad), math.Sin(rad)
	px, py := float64(l.Pivot[0]), float64(l.Pivot[1])

	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, c := range [][2]float64{
		{0, 0}, {float64(b.Dx()), 0}, {0, float64(b.Dy())}, {float64(b.Dx()), float64(b.Dy())},
	} {
		x, y := forward(c[0], c[1], px, py, cos, sin, t)
		x, y = x+float64(l.At[0]), y+float64(l.At[1])
		minX, minY = math.Min(minX, x), math.Min(minY, y)
		maxX, maxY = math.Max(maxX, x), math.Max(maxY, y)
	}
	return image.Rect(int(math.Floor(minX)), int(math.Floor(minY)),
		int(math.Ceil(maxX))+1, int(math.Ceil(maxY))+1)
}

// drawTransformed blits art under a transform: rotation and scale about the
// layer's pivot, then the offset.
//
// Sampling is bilinear, and that is not a quality nicety — it is what makes
// interpolation mean anything. A breath of two pixels over two seconds
// passes through offsets like 0.19 and 0.68, and a renderer that snaps to
// whole pixels turns all of that into three positions with long pauses
// between them: the motion is computed smoothly and then thrown away at the
// last step, which reads as a twitch rather than a breath. Sub-pixel
// placement is the entire difference.
//
// Whole-pixel placements skip the sampling entirely, so a layer that has
// merely been moved an integer distance still costs a plain blit.
func drawTransformed(dst *image.NRGBA, art *image.NRGBA, l Layer, t Transform) {
	b := art.Bounds()
	rad := t.Rotate * math.Pi / 180
	cos, sin := math.Cos(rad), math.Sin(rad)
	px, py := float64(l.Pivot[0]), float64(l.Pivot[1])

	area := layerBounds(b, l, t).Intersect(dst.Bounds())

	alpha := 1.0
	if t.Opacity != nil {
		alpha = *t.Opacity
	}
	// A whole-pixel translation needs no resampling at all.
	whole := t.Rotate == 0 && t.Scale == 1 &&
		t.Offset[0] == math.Trunc(t.Offset[0]) && t.Offset[1] == math.Trunc(t.Offset[1])

	for y := area.Min.Y; y < area.Max.Y; y++ {
		for x := area.Min.X; x < area.Max.X; x++ {
			sx, sy := inverse(
				float64(x)-float64(l.At[0]), float64(y)-float64(l.At[1]),
				px, py, cos, sin, t)
			var c color.NRGBA
			if whole {
				ix, iy := int(math.Round(sx)), int(math.Round(sy))
				if ix < 0 || iy < 0 || ix >= b.Dx() || iy >= b.Dy() {
					continue
				}
				c = art.NRGBAAt(ix, iy)
			} else {
				c = sampleBilinear(art, sx, sy)
			}
			if c.A == 0 {
				continue
			}
			if alpha < 1 {
				c.A = uint8(float64(c.A) * alpha)
			}
			blend(dst, x, y, c)
		}
	}
}

// sampleBilinear reads art at a fractional position, blending the four
// neighbouring pixels. Colour is weighted by alpha as well as by distance,
// so a semi-transparent edge does not drag its neighbours' colour into the
// visible part — the usual halo around naively filtered sprite art.
func sampleBilinear(art *image.NRGBA, sx, sy float64) color.NRGBA {
	b := art.Bounds()
	x0, y0 := int(math.Floor(sx)), int(math.Floor(sy))
	fx, fy := sx-float64(x0), sy-float64(y0)

	var r, g, bl, a, wsum float64
	for _, p := range [4]struct {
		dx, dy int
		w      float64
	}{
		{0, 0, (1 - fx) * (1 - fy)},
		{1, 0, fx * (1 - fy)},
		{0, 1, (1 - fx) * fy},
		{1, 1, fx * fy},
	} {
		if p.w == 0 {
			continue
		}
		x, y := x0+p.dx, y0+p.dy
		if x < 0 || y < 0 || x >= b.Dx() || y >= b.Dy() {
			continue
		}
		c := art.NRGBAAt(x, y)
		ca := float64(c.A) / 255
		r += float64(c.R) * p.w * ca
		g += float64(c.G) * p.w * ca
		bl += float64(c.B) * p.w * ca
		a += float64(c.A) * p.w
		wsum += p.w * ca
	}
	if a <= 0 {
		return color.NRGBA{}
	}
	if wsum == 0 {
		return color.NRGBA{A: uint8(a)}
	}
	return color.NRGBA{
		R: uint8(math.Round(r / wsum)),
		G: uint8(math.Round(g / wsum)),
		B: uint8(math.Round(bl / wsum)),
		A: uint8(math.Round(a)),
	}
}

// forward maps a point in art space to shell space (before At).
func forward(x, y, px, py, cos, sin float64, t Transform) (float64, float64) {
	x, y = (x-px)*t.Scale, (y-py)*t.Scale
	return px + x*cos - y*sin + t.Offset[0], py + x*sin + y*cos + t.Offset[1]
}

// inverse maps a point in shell space (after At) back into art space.
func inverse(x, y, px, py, cos, sin float64, t Transform) (float64, float64) {
	x, y = x-t.Offset[0]-px, y-t.Offset[1]-py
	rx, ry := x*cos+y*sin, -x*sin+y*cos
	if t.Scale == 0 {
		return -1, -1
	}
	return rx/t.Scale + px, ry/t.Scale + py
}

// blend puts a non-premultiplied pixel over the destination.
func blend(dst *image.NRGBA, x, y int, c color.NRGBA) {
	if c.A == 255 {
		dst.SetNRGBA(x, y, c)
		return
	}
	d := dst.NRGBAAt(x, y)
	a := uint32(c.A)
	inv := 255 - a
	// Straight-alpha over; both sides are non-premultiplied here.
	outA := a + uint32(d.A)*inv/255
	if outA == 0 {
		return
	}
	mix := func(sc, dc uint8) uint8 {
		return uint8((uint32(sc)*a + uint32(dc)*uint32(d.A)*inv/255) / outA)
	}
	dst.SetNRGBA(x, y, color.NRGBA{
		R: mix(c.R, d.R), G: mix(c.G, d.G), B: mix(c.B, d.B), A: uint8(outA),
	})
}
