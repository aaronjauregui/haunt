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
	current map[string]string // layer name -> variant

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

// key identifies the current variant selection for caching.
func (s *Shell) key() string {
	parts := make([]string, 0, len(s.layers))
	for _, l := range s.layers {
		parts = append(parts, l.Name+"="+s.current[l.Name])
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
		art := l.variants[s.current[l.Name]]
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
		art := l.variants[s.current[l.Name]]
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
