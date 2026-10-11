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
//	+ clips                → animation by swapping variants on a clock
//	+ transforms           → interpolated motion of layers
//	+ masks                → a fixed canvas, and a place for each part
//
// Layers carry `parent` and `pivot`: a rig where rotating an upper arm
// carries the forearm is part of the format, because it cannot be
// retrofitted onto shells authored without it.
//
// Masks are the opt-in for a shell whose parts must stay put. A mask is a
// painted picture of where a layer's art may go: every variant of a masked
// layer is checked against it at load, so a mouth drawn by a different hand
// (or a different generation) cannot land somewhere else on the face. The
// masks also fix the canvas, and the layers without one composite into the
// base (Shell.Base), the ghost with no face and nothing worn.
//
// Everything in a manifest is in art pixels, and a shell has a scale at
// which that art is drawn. The loader converts once, resampling the art
// and scaling every coordinate, so nothing downstream of it knows the art
// was ever a different size: a ghost painted at 1024 pixels sits on the
// desktop at 256, and the full-resolution art stays on disk.
package shell

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"maps"
	"math"
	"path/filepath"
	"slices"
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
	// the layer's own origin.
	Pivot [2]int `toml:"pivot"`
	// Parent nests this layer's transform inside another's, so a rotating
	// arm carries its hand.
	Parent string `toml:"parent"`
	// Region names the part of the ghost this layer represents (head,
	// face, hand). Clicks report the topmost region they land on, which is
	// how a brain can tell being patted from being poked.
	Region string `toml:"region"`
	// Mask binds the layer to one of the shell's masks: every variant's art
	// must stay inside it, and the layer may show nothing (variant "none").
	// A masked layer with no image and no variants is an empty slot, whose
	// only variant is "none" until art is drawn for it.
	Mask string `toml:"mask"`
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
	// Hits are named rectangles, checked before the art. Hand-drawn shells
	// name their parts on the layers that draw them, which is better —
	// a region follows the drawing and needs no maintenance. Imported
	// shells often cannot: SERIKO describes its hit areas as bare
	// rectangles with no layer of their own (internal/seriko), and the
	// alternative would be inventing art to carry them.
	Hits []Hit `toml:"hit"`
	// Scale is how large the art is drawn: 0.25 draws it at a quarter of
	// its pixel size. Zero means 1. Everything in the manifest is in art
	// pixels, and the loader converts once.
	Scale float64 `toml:"scale"`
	// Masks maps a mask name to a PNG marking where art may go. Declaring
	// any fixes the canvas to their size; "silhouette" is required.
	Masks map[string]string `toml:"masks"`
	// Note is the paper the ghost's desktop notes are written on, drawn
	// at the shell's scale like the rest of the art. Empty means the
	// driver's own plain sheet (balloon.RenderNote).
	Note string `toml:"note"`
}

// Files lists every path the manifest names — the sprite, each layer's
// image and variants, and the masks — so a caller can check that none of
// them leaves the ghost directory.
func (m Manifest) Files() []string {
	var out []string
	if m.Sprite != "" {
		out = append(out, m.Sprite)
	}
	for _, l := range m.Layers {
		if l.Image != "" {
			out = append(out, l.Image)
		}
		for _, name := range sortedKeys(l.Variants) {
			out = append(out, l.Variants[name])
		}
	}
	for _, name := range sortedKeys(m.Masks) {
		out = append(out, m.Masks[name])
	}
	if m.Note != "" {
		out = append(out, m.Note)
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// LoadOptions are the person's adjustments to how a shell is drawn.
type LoadOptions struct {
	// ScaleFactor multiplies the shell's own scale. Zero means 1.
	ScaleFactor float64
}

// LayerInfo describes a loaded layer, for tools that report on a shell.
type LayerInfo struct {
	Name, Mask, Default, Region, Parent string
	// Variants is sorted; "" is a single-image layer's appearance.
	Variants []string
}

// MoodPoses are the pose names the driver knows how to choose by itself,
// from the mood of a line (see internal/ghost/expression.go). A shell that
// names its poses these gets an expression that follows what it says,
// without its brain asking for anything; a shell that names them otherwise
// keeps whatever face its brain sets, which for a brain that sets none is
// one face forever.
//
// Declared here rather than in the driver because it is a fact about the
// shell format — an author choosing names, or a converter inventing them,
// needs to know which ones mean something. A test in internal/ghost keeps
// this and the mood table from drifting apart.
var MoodPoses = []string{"happy", "sad", "surprised", "asleep", "thinking"}

// Hit is a named rectangle a person can poke, independent of what is drawn
// there. Declaration order decides ties, first one winning, which is what
// the formats that use rectangles already mean by it.
type Hit struct {
	// Region is the name reported for a poke inside it.
	Region string `toml:"region"`
	// At is the top-left corner in shell coordinates (art pixels in a
	// manifest, like every coordinate there).
	At [2]int `toml:"at"`
	// Size is width and height. A zero or negative dimension disables the
	// rectangle, which is how shells switch one off per pose.
	Size [2]int `toml:"size"`
}

// rect is the hit area as a rectangle, empty when disabled.
func (h Hit) rect() image.Rectangle {
	if h.Size[0] <= 0 || h.Size[1] <= 0 {
		return image.Rectangle{}
	}
	return image.Rect(h.At[0], h.At[1], h.At[0]+h.Size[0], h.At[1]+h.Size[1])
}

// loaded is a layer with its art decoded.
type loaded struct {
	Layer
	// variants holds every decoded appearance, keyed by variant name. A
	// single-image layer stores it under "". A masked layer's "none" is a
	// zero-size image, so everything that draws or measures a layer has to
	// cope with art that covers nothing.
	variants map[string]*image.NRGBA
	order    int
	// pivot is Pivot in shell pixels. It is fractional once the art is
	// scaled, because the art's origin moves to a whole pixel and the
	// pivot has to stay on the same point of the drawing.
	pivot [2]float64
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
	// hits are named rectangles from the manifest, checked before the art
	// (see RegionAt).
	hits []Hit

	// cache holds composited poses. A ghost that is not changing
	// expression composites nothing.
	cache map[string]*sprite.Sprite

	// scale is the effective scale the art was drawn at, and artW and
	// artH the canvas before it.
	scale      float64
	artW, artH int
	// masks and base stay at art resolution, for the tools that check a
	// shell and for anything that paints on it later. Both are empty in a
	// shell without masks.
	masks map[string]*image.Gray
	base  *image.NRGBA
	// note is the paper desktop notes are drawn on, at the shell's scale;
	// nil when the shell ships none.
	note *image.NRGBA
}

// Load is LoadWith with zero options.
func Load(dir string, m Manifest) (*Shell, error) {
	return LoadWith(dir, m, LoadOptions{})
}

// maxScale is the largest effective scale. Beyond it a shell is a wall of
// pixels rather than a companion, and an art mistake rather than a choice.
const maxScale = 4

// LoadWith loads any shell: layers, a single sprite, or (when the manifest
// names neither) the built-in placeholder at the effective scale. It does
// not modify m.
//
// The effective scale is the shell's own times opts.ScaleFactor, held to at
// most 4 rather than refused: the factor is the person's, and their config
// should not stop a ghost appearing. Shell.Scale reports what was used, so
// a caller that wants to say the factor was cut short compares it with
// what it asked for.
//
// Everything that can be checked without decoding art is checked before
// the placeholder is returned, so a manifest that says nothing but a bad
// scale is still an error.
func LoadWith(dir string, m Manifest, opts LoadOptions) (*Shell, error) {
	// The loader rewrites clips, hits and layers as it scales them; working
	// on a copy keeps the caller's manifest as written, so loading it twice
	// gives the same shell.
	m = m.clone()
	for _, p := range m.Files() {
		if !filepath.IsLocal(p) {
			return nil, fmt.Errorf("path %q must be relative and stay inside the ghost directory", p)
		}
	}
	// Written as a positive range test so that NaN fails it too.
	if !(m.Scale >= 0 && m.Scale <= maxScale) {
		return nil, fmt.Errorf("shell scale %g must be above 0 and at most %d", m.Scale, maxScale)
	}
	if !(opts.ScaleFactor >= 0) {
		return nil, fmt.Errorf("shell scale factor %g must not be negative", opts.ScaleFactor)
	}
	if len(m.Masks) > 0 && len(m.Layers) == 0 {
		return nil, fmt.Errorf("[shell.masks] needs [[shell.layer]]")
	}
	scale := effectiveScale(m.Scale, opts.ScaleFactor)

	var s *Shell
	var err error
	switch {
	case m.Sprite == "" && len(m.Layers) == 0:
		s = placeholder(scale)
	case len(m.Layers) == 0:
		s, err = loadSingle(dir, m.Sprite, scale)
	default:
		s, err = load(dir, m, scale)
	}
	if err != nil {
		return nil, err
	}
	// The note paper is art like the rest, so it is read and scaled here,
	// and a missing file is a load error rather than a plain sheet
	// appearing the first time the ghost leaves a note.
	if m.Note != "" {
		img, err := loadImage(dir, m.Note)
		if err != nil {
			return nil, fmt.Errorf("shell note: %w", err)
		}
		s.note, _ = resample(img, [2]int{}, scale)
	}
	return s, nil
}

// effectiveScale combines the shell's scale with the person's factor, zero
// meaning 1 for both, and keeps the result within (0, maxScale].
func effectiveScale(shell, factor float64) float64 {
	if shell == 0 {
		shell = 1
	}
	if factor == 0 {
		factor = 1
	}
	s := shell * factor
	switch {
	case s > maxScale:
		return maxScale
	case s <= 0:
		// Only two tiny factors underflowing can get here; the smallest
		// scale that still draws something is as close as it gets.
		return math.SmallestNonzeroFloat64
	}
	return s
}

// placeholder is the built-in ghost at a scale. It is drawn at its size in
// code rather than resampled, which is why it has a floor: below 16 pixels
// the drawing stops being a face.
func placeholder(scale float64) *Shell {
	s := Builtin(max(16, int(math.Round(160*scale))))
	s.scale = scale
	s.artW, s.artH = 160, 160
	return s
}

// clone copies everything the loader might rewrite, down to the maps a
// clip's keys hold.
func (m Manifest) clone() Manifest {
	out := m
	out.Layers = make([]Layer, len(m.Layers))
	for i, l := range m.Layers {
		l.Variants = maps.Clone(l.Variants)
		out.Layers[i] = l
	}
	if m.Poses != nil {
		out.Poses = make(map[string]map[string]string, len(m.Poses))
		for name, sel := range m.Poses {
			out.Poses[name] = maps.Clone(sel)
		}
	}
	out.Clips = make([]Clip, len(m.Clips))
	for i, c := range m.Clips {
		keys := make([]Key, len(c.Keys))
		for j, k := range c.Keys {
			k.Set = maps.Clone(k.Set)
			k.Move = maps.Clone(k.Move)
			keys[j] = k
		}
		c.Keys = keys
		out.Clips[i] = c
	}
	out.Hits = slices.Clone(m.Hits)
	out.Masks = maps.Clone(m.Masks)
	return out
}

// load decodes a layered manifest relative to dir, at art resolution, and
// checks it there; only then is the art scaled for display.
func load(dir string, m Manifest, scale float64) (*Shell, error) {
	s := &Shell{
		poses:   m.Poses,
		current: map[string]string{},
		cache:   map[string]*sprite.Sprite{},
		scale:   scale,
	}
	hasMasks := len(m.Masks) > 0
	names := map[string]bool{}
	for i, l := range m.Layers {
		if l.Name == "" {
			return nil, fmt.Errorf("shell layer %d has no name", i)
		}
		if names[l.Name] {
			return nil, fmt.Errorf("shell layer %q declared twice", l.Name)
		}
		names[l.Name] = true

		masked := l.Mask != ""
		if masked {
			if !hasMasks {
				return nil, fmt.Errorf("layer %q names mask %q but the shell declares no masks", l.Name, l.Mask)
			}
			if _, ok := m.Masks[l.Mask]; !ok {
				return nil, fmt.Errorf("shell layer %q names mask %q, which [shell.masks] does not declare", l.Name, l.Mask)
			}
		}
		if l.Image == "" && len(l.Variants) == 0 && !masked {
			return nil, fmt.Errorf("shell layer %q has no image or variants", l.Name)
		}
		if l.Image != "" && len(l.Variants) > 0 {
			return nil, fmt.Errorf("shell layer %q has both image and variants", l.Name)
		}

		ld := &loaded{
			Layer:    l,
			variants: map[string]*image.NRGBA{},
			order:    i,
			pivot:    [2]float64{float64(l.Pivot[0]), float64(l.Pivot[1])},
		}
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
		}
		if masked {
			// Every masked layer can show nothing. A declared "none" is
			// kept: it is ordinary art that happens to have that name.
			if _, ok := ld.variants[none]; !ok {
				ld.variants[none] = image.NewNRGBA(image.Rectangle{})
			}
			if l.Image == "" && ld.Default == "" {
				ld.Default = none
			}
		}
		if l.Image == "" {
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

	if hasMasks {
		if err := s.loadMasks(dir, m.Masks); err != nil {
			return nil, err
		}
		if err := s.checkArt(); err != nil {
			return nil, err
		}
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

	// Named rectangles, if the manifest declared any. Validated here so a
	// typo in a region name is a load error rather than a poke that
	// silently reports nothing.
	for i, h := range m.Hits {
		if h.Region == "" {
			return nil, fmt.Errorf("hit %d has no region name", i)
		}
		s.hits = append(s.hits, h)
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

	if hasMasks {
		s.base = s.composeBase()
	} else {
		s.artW, s.artH = s.canvas()
	}
	if scale != 1 {
		s.rescale(scale)
	}
	if hasMasks {
		s.w = int(math.Round(float64(s.artW) * scale))
		s.h = int(math.Round(float64(s.artH) * scale))
	} else {
		s.w, s.h = s.canvas()
	}

	if m.Default != "" {
		if !s.SetPose(m.Default) {
			return nil, fmt.Errorf("default_pose %q is not a declared pose", m.Default)
		}
	}
	return s, nil
}

// none is the variant every masked layer has: nothing drawn at all. Poses
// and clips select it like any other, which is how a face comes off or a
// hat is not worn.
const none = "none"

// loadSingle wraps a one-image shell as a one-layer stack, so everything
// downstream sees the same thing.
func loadSingle(dir, path string, scale float64) (*Shell, error) {
	img, err := loadImage(dir, path)
	if err != nil {
		return nil, err
	}
	artW, artH := img.Bounds().Dx(), img.Bounds().Dy()
	img, _ = resample(img, [2]int{}, scale)
	b := img.Bounds()
	return &Shell{
		w: b.Dx(), h: b.Dy(),
		layers: []*loaded{{
			Layer:    Layer{Name: "body"},
			variants: map[string]*image.NRGBA{"": img},
		}},
		current: map[string]string{"body": ""},
		cache:   map[string]*sprite.Sprite{},
		scale:   scale,
		artW:    artW, artH: artH,
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
// the compositor.
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

// canvas is the union of every layer's placed bounds. Art that covers
// nothing does not stretch it, wherever its layer is placed.
func (s *Shell) canvas() (w, h int) {
	for _, l := range s.layers {
		for _, img := range l.variants {
			b := img.Bounds()
			if b.Empty() {
				continue
			}
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

// Scale is the effective scale the art is drawn at: the shell's own times
// the person's factor, within the limit LoadWith holds it to. The
// placeholder reports the scale it was asked for, although it is drawn at
// no less than 16 pixels.
func (s *Shell) Scale() float64 {
	if s.scale == 0 {
		return 1
	}
	return s.scale
}

// ArtSize is the canvas in art pixels, before scaling: the masks' size
// when the shell declares them, the union of the layers otherwise.
func (s *Shell) ArtSize() (w, h int) {
	if s.artW == 0 && s.artH == 0 {
		return s.w, s.h
	}
	return s.artW, s.artH
}

// Base is the ghost with no face and nothing worn: every unmasked layer at
// its default, untransformed, composited at art resolution on the canvas.
// Nil for a shell without masks. Each call returns a fresh copy, so the
// caller may paint on it.
func (s *Shell) Base() *image.NRGBA {
	if s.base == nil {
		return nil
	}
	out := *s.base
	out.Pix = slices.Clone(s.base.Pix)
	return &out
}

// composeBase draws the unmasked layers at their defaults, in z order, on
// the art-resolution canvas. It runs before the art is scaled.
func (s *Shell) composeBase() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, s.artW, s.artH))
	for _, l := range s.layers {
		if l.Mask != "" {
			continue
		}
		art := l.variants[l.Default]
		b := art.Bounds()
		at := image.Rect(l.At[0], l.At[1], l.At[0]+b.Dx(), l.At[1]+b.Dy())
		draw.Draw(img, at, art, b.Min, draw.Over)
	}
	return img
}

// Note is the paper the shell ships for desktop notes, at the shell's
// scale, or nil. It is shared, and callers must not modify it.
func (s *Shell) Note() *sprite.Sprite {
	if s.note == nil {
		return nil
	}
	return &sprite.Sprite{Img: s.note}
}

// Masks lists the declared mask names, sorted.
func (s *Shell) Masks() []string {
	return slices.Sorted(maps.Keys(s.masks))
}

// Mask is a declared mask at art resolution, nil if there is none by that
// name. It is shared, and callers must not modify it.
func (s *Shell) Mask(name string) *image.Gray { return s.masks[name] }

// Layers describes the layers in z order.
func (s *Shell) Layers() []LayerInfo {
	out := make([]LayerInfo, 0, len(s.layers))
	for _, l := range s.layers {
		out = append(out, LayerInfo{
			Name: l.Name, Mask: l.Mask, Default: l.Default,
			Region: l.Region, Parent: l.Parent,
			Variants: slices.Sorted(maps.Keys(l.variants)),
		})
	}
	return out
}

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
		case TriggerAlways, TriggerSometimes, TriggerRarely, TriggerTalk, TriggerRunOnce, TriggerManual, "":
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
		drawTransformed(img, art, l.At, l.pivot, t)
	}
	sp := &sprite.Sprite{Img: img}
	s.cache[k] = sp
	return sp
}

// RegionAt names the part of the ghost at a point in shell coordinates:
// the topmost layer that both declares a region and is opaque there, or a
// declared hit rectangle. Empty when the point hits no named part.
//
// Rectangles are asked first. A shell that declares them has said where its
// parts are outright, and the art it draws them over is usually one flat
// picture with no layer to attach a region to — so falling through to the
// pixels would find the body layer's region for every poke and report the
// same answer everywhere.
func (s *Shell) RegionAt(x, y int) string {
	p := image.Pt(x, y)
	for _, h := range s.hits {
		if h.Region == "" {
			continue
		}
		if r := h.rect(); !r.Empty() && p.In(r) {
			return h.Region
		}
	}
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
// A keyframe is a struct rather than a bare variant selection because it
// carries eased transforms (`move`) alongside `set`.
type Clip struct {
	Name string `toml:"name"`
	// Trigger says who starts it:
	//   always    — restart as soon as it ends
	//   sometimes — at random intervals of a few seconds
	//   rarely    — at random intervals of a minute or so
	//   talk      — loops for as long as the ghost is speaking
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
	// Offset shifts the layer, in shell pixels. A manifest gives it in art
	// pixels, and the loader scales it with the art; rotation, scale and
	// opacity are unitless and stay as written.
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
	TriggerTalk      = "talk"
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

// Selection is which variant each layer is showing.
func (st State) Selection() map[string]string { return st.selection }

// Moves is where each layer has been placed.
func (st State) Moves() map[string]Transform { return st.moves }

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
			out = out.Union(layerBounds(art.Bounds(), l.At, l.pivot, st.t))
		}
	}
	return out.Intersect(whole)
}

// layerBounds is where a layer's art, of bounds b placed at at, lands in
// shell coordinates under a transform about pivot. Art that covers nothing
// lands nowhere, however it is moved.
func layerBounds(b image.Rectangle, at [2]int, pivot [2]float64, t Transform) image.Rectangle {
	if b.Empty() {
		return image.Rectangle{}
	}
	t = t.normalized()
	if !t.moved() {
		return b.Add(image.Pt(at[0], at[1]))
	}
	rad := t.Rotate * math.Pi / 180
	cos, sin := math.Cos(rad), math.Sin(rad)
	px, py := pivot[0], pivot[1]

	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, c := range [][2]float64{
		{0, 0}, {float64(b.Dx()), 0}, {0, float64(b.Dy())}, {float64(b.Dx()), float64(b.Dy())},
	} {
		x, y := forward(c[0], c[1], px, py, cos, sin, t)
		x, y = x+float64(at[0]), y+float64(at[1])
		minX, minY = math.Min(minX, x), math.Min(minY, y)
		maxX, maxY = math.Max(maxX, x), math.Max(maxY, y)
	}
	return image.Rect(int(math.Floor(minX)), int(math.Floor(minY)),
		int(math.Ceil(maxX))+1, int(math.Ceil(maxY))+1)
}

// drawTransformed blits art placed at at under a transform: rotation and
// scale about pivot, then the offset. The pivot is fractional in a scaled
// shell, which is why it is passed in rather than read from the Layer.
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
func drawTransformed(dst *image.NRGBA, art *image.NRGBA, at [2]int, pivot [2]float64, t Transform) {
	b := art.Bounds()
	if b.Empty() {
		return
	}
	rad := t.Rotate * math.Pi / 180
	cos, sin := math.Cos(rad), math.Sin(rad)
	px, py := pivot[0], pivot[1]

	area := layerBounds(b, at, pivot, t).Intersect(dst.Bounds())

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
				float64(x)-float64(at[0]), float64(y)-float64(at[1]),
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
