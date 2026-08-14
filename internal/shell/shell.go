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
	"image/draw"
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
	// pose.
	overrides map[string]string
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

// key identifies the current variant selection for caching.
func (s *Shell) key() string {
	parts := make([]string, 0, len(s.layers))
	for _, l := range s.layers {
		parts = append(parts, l.Name+"="+s.selection(l.Name))
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
		at := image.Rect(l.At[0], l.At[1], l.At[0]+b.Dx(), l.At[1]+b.Dy())
		draw.Draw(img, at, art, b.Min, draw.Over)
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
	// mouth alone.
	Set map[string]string `toml:"set"`
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
	if len(c.Keys) == 0 {
		return nil, 0, true
	}
	set = map[string]string{}
	for _, k := range c.Keys {
		if k.At > elapsed {
			return set, k.At - elapsed, false
		}
		// Keyframes accumulate: a later key that mentions only the mouth
		// leaves an earlier key's eyes in place.
		for layer, variant := range k.Set {
			set[layer] = variant
		}
	}
	return set, 0, true
}
