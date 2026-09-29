package artimport

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/aaronjauregui/haunt/internal/shell"
)

// Converting a drawing into a shell.
//
// The rules are meant to be guessable from the other side: an artist should
// be able to look at their layer palette and know what they will get.
//
//   - A top-level layer becomes a layer with one appearance.
//   - A group becomes a layer whose children are its variants — which is
//     what a group of `open` / `closed` / `happy` under `eyes` already
//     means to the person who drew it.
//   - A flat name with a slash (`eyes/closed`) means the same thing, for
//     tools and habits that do not use groups.
//   - Visibility picks the default: the visible child of a group is what
//     the ghost wears when no pose says otherwise. A hidden *top-level*
//     layer is left out entirely, because that is where sketches, rulers
//     and reference photos live.
//   - Drawing order becomes z order.
//   - A top-level group called `masks` holds masks, not art: each layer in
//     it marks where one part of the ghost may be drawn, `silhouette` the
//     whole of it. A layer or group named like a mask is bound to it, and
//     a mask with nothing drawn for it becomes an empty slot.
//
// And every layer is cropped to the pixels it actually covers, with the
// trim folded into its `at`. That is not tidiness: damage tracking repaints
// the bounds of what changed, so a blink drawn on a full-canvas sheet
// repaints the whole ghost. It is the one rule a hand-authoring artist
// reliably gets wrong, and doing it here means they never have to know.

// piece is one layer's art as it survived the naming and visibility rules,
// before anything is cropped.
type piece struct {
	art     Art
	layer   string
	variant string
	bounds  image.Rectangle // in canvas coordinates, tight to the marks
}

// Result is a converted shell: the manifest section and the files it names.
type Result struct {
	// Shell is the [shell] section, ready to write into a ghost.toml.
	Shell shell.Manifest
	// Files maps a path relative to the ghost directory to PNG bytes.
	Files map[string][]byte
	// Notes are what the conversion decided that the artist might want to
	// know about: layers left out, names changed.
	Notes []string
	// Masks counts the mask files among Files, which are not layer art.
	Masks int
	// W and H are the canvas in art pixels: the drawing's size when it has
	// masks, since the masks fix the canvas, and otherwise the extent of
	// the layers from the origin, which is what the loader makes it.
	W, H int
}

// nameSafe is what a layer or variant name may contain once tidied: the
// names end up in TOML keys, file names and pose selectors, and a layer
// called "Left Arm (WIP)" should not become three problems.
var nameSafe = regexp.MustCompile(`[^a-z0-9]+`)

// masksGroup is the name, once tidied, of the group whose layers are masks
// rather than art. Flat `masks/<name>` layers mean the same, as `eyes/open`
// does for variants.
const masksGroup = "masks"

// Convert turns a layered drawing into a shell. dir is the subdirectory
// under the ghost that art is written to.
func Convert(st *Stack, dir string) (*Result, error) {
	if len(st.Layers) == 0 {
		return nil, fmt.Errorf("the drawing has no layers")
	}
	if dir == "" {
		dir = "shell"
	}
	res := &Result{Files: map[string][]byte{}}

	masks, err := res.readMasks(st, dir)
	if err != nil {
		return nil, err
	}

	// First pass: decide what each piece of art is called and what it
	// covers. Nothing is cropped yet, because a layer's variants have to be
	// cropped together — see below.
	var pieces []piece
	order := []string{}
	seen := map[string]bool{}
	for _, art := range st.Layers {
		layerName, variantName := split(art)
		if layerName == masksGroup {
			continue // read above; masks are not drawn
		}
		if layerName == "" {
			res.note("skipped a layer with no usable name (%q)", art.Name)
			continue
		}
		// Hidden top-level art is a sketch or a reference; hidden art in a
		// group is simply a variant that is not currently worn.
		if variantName == "" && !art.Visible {
			res.note("left out %q: hidden in the drawing", art.Name)
			continue
		}
		bounds, ok := marks(art.Img)
		if !ok {
			res.note("left out %q: nothing drawn on it", art.Name)
			continue
		}
		if variantName == "" && art.Name != layerName {
			res.note("renamed %q to %q", art.Name, layerName)
		}
		pieces = append(pieces, piece{art: art, layer: layerName, variant: variantName, bounds: bounds})
		if !seen[layerName] {
			seen[layerName] = true
			order = append(order, layerName)
		}
	}
	if len(pieces) == 0 {
		return nil, fmt.Errorf("nothing to import: every layer was empty or hidden")
	}

	// A layer's variants are swapped in place by a pose, so they share one
	// origin: the shell format has a single `at` per layer. Cropping each
	// variant to its own marks would therefore shift the art — a closed eye
	// drawn as a thin line would jump to where the open eye's top pixel
	// was. So every variant is cropped to the union of the group, which is
	// still tight around the eyes rather than around the ghost.
	box := map[string]image.Rectangle{}
	for _, p := range pieces {
		// Canvas coordinates: the layer's own placement plus where the
		// marks sit inside its image.
		b := p.bounds.Add(image.Pt(p.art.X-p.art.Img.Bounds().Min.X, p.art.Y-p.art.Img.Bounds().Min.Y))
		if cur, ok := box[p.layer]; ok {
			box[p.layer] = cur.Union(b)
		} else {
			box[p.layer] = b
		}
	}

	type variant struct{ name, file string }
	type entry struct {
		variants []variant
		def      string
		single   string
	}
	entries := map[string]*entry{}
	used := map[string]bool{}
	tol := shell.Tolerance(st.W, st.H)
	for _, p := range pieces {
		file := dir + "/" + p.layer
		if p.variant != "" {
			file += "-" + p.variant
		}
		file += ".png"
		if used[file] {
			res.note("skipped a second %q: the name is already taken", p.art.Name)
			continue
		}
		used[file] = true

		cropped := cropTo(p.art, box[p.layer])
		// The loader refuses masked art that strays outside its mask, so
		// the same check runs here, before any file is written: a spill
		// is found while the drawing is open, not when the ghost fails to
		// appear.
		if m := masks[p.layer]; m != nil {
			if err := shell.CheckContained(m, cropped, box[p.layer].Min, tol); err != nil {
				if p.variant == "" {
					return nil, fmt.Errorf("layer %q is drawn outside its mask: %w", p.layer, err)
				}
				return nil, fmt.Errorf("layer %q, variant %q, is drawn outside its mask: %w", p.layer, p.variant, err)
			}
		}
		buf := &bytes.Buffer{}
		if err := png.Encode(buf, cropped); err != nil {
			return nil, fmt.Errorf("encoding %s: %w", file, err)
		}
		res.Files[file] = buf.Bytes()

		e := entries[p.layer]
		if e == nil {
			e = &entry{}
			entries[p.layer] = e
		}
		if p.variant == "" {
			e.single = file
			continue
		}
		e.variants = append(e.variants, variant{p.variant, file})
		if p.art.Visible && e.def == "" {
			e.def = p.variant
		}
	}

	hasLayer := map[string]bool{}
	for z, name := range order {
		e := entries[name]
		if e == nil {
			continue
		}
		b := box[name]
		l := shell.Layer{Name: name, Z: z, At: [2]int{b.Min.X, b.Min.Y}}
		if masks[name] != nil {
			l.Mask = name
		}
		switch {
		case len(e.variants) > 0:
			l.Variants = map[string]string{}
			for _, v := range e.variants {
				l.Variants[v.name] = v.file
			}
			l.Default = e.def
			switch {
			case l.Default != "":
			case l.Mask != "":
				// A masked layer may show nothing, and a group the artist
				// hid entirely is the plainest way of saying so: a hat
				// that is off until a pose puts it on.
				l.Default = "none"
				res.note("layer %q had no visible variant; it shows nothing until a pose picks one", name)
			default:
				// Nothing in the group was visible; the first variant is as
				// good a guess as any, and better than a layer with no
				// default at all.
				l.Default = e.variants[0].name
				res.note("layer %q had no visible variant; defaulting to %q", name, l.Default)
			}
		case e.single != "":
			l.Image = e.single
		default:
			continue
		}
		res.Shell.Layers = append(res.Shell.Layers, l)
		hasLayer[name] = true
		if b.Max.X > res.W {
			res.W = b.Max.X
		}
		if b.Max.Y > res.H {
			res.H = b.Max.Y
		}
	}

	// A mask with no art of its own is a slot: a place where something
	// goes that nobody has drawn yet. It still gets a layer, empty, so
	// that a pose, a clip or a later drawing has somewhere to put it. It
	// sits above everything, since what fills a slot is usually worn, and
	// moves with the body when there is one.
	z := len(order)
	for _, name := range sortedNames(masks) {
		if name == "silhouette" || hasLayer[name] {
			continue
		}
		l := shell.Layer{Name: name, Mask: name, Z: z}
		if hasLayer["body"] {
			l.Parent = "body"
		}
		z++
		res.Shell.Layers = append(res.Shell.Layers, l)
		res.note("mask %q has no layer drawn for it; added an empty one", name)
	}
	if len(masks) > 0 {
		res.W, res.H = st.W, st.H
	}
	return res, nil
}

// readMasks collects the masks group: each mask read the way the loader
// reads it, written uncropped at the full size of the drawing, and named
// in the manifest. Uncropped because the masks' shared size is what fixes
// the canvas, so a mask cropped to its marks would be a different shell.
// Visibility does not matter here: masks are never drawn, and an artist
// hides them to see the art underneath.
func (r *Result) readMasks(st *Stack, dir string) (map[string]*image.Gray, error) {
	masks := map[string]*image.Gray{}
	for _, art := range st.Layers {
		layer, name := split(art)
		if layer != masksGroup {
			continue
		}
		switch {
		case art.Depth > 1:
			return nil, fmt.Errorf("%q is in a group inside %q; each mask is one layer directly in the masks group", art.Name, art.Group)
		case name == "" && art.Group == "" && !strings.Contains(art.Name, "/"):
			return nil, fmt.Errorf("a layer is named %q; masks are the layers inside a group of that name, one per mask", art.Name)
		case name == "":
			return nil, fmt.Errorf("mask %q has no usable name; mask names are letters, digits and dashes", art.Name)
		case masks[name] != nil:
			return nil, fmt.Errorf("two masks are named %q", name)
		}
		if st.W <= 0 || st.H <= 0 {
			return nil, fmt.Errorf("the drawing does not say how big its canvas is, and masks need that")
		}
		full := image.NewNRGBA(image.Rect(0, 0, st.W, st.H))
		src := art.Img.Bounds()
		draw.Draw(full, image.Rectangle{Min: image.Pt(art.X, art.Y), Max: image.Pt(art.X+src.Dx(), art.Y+src.Dy())},
			art.Img, src.Min, draw.Src)
		m := shell.MaskFromImage(full)
		if !anyInside(m) {
			return nil, fmt.Errorf("mask %q marks nothing: no pixel is bright and opaque enough to count as inside", name)
		}
		file := dir + "/masks/" + name + ".png"
		buf := &bytes.Buffer{}
		if err := png.Encode(buf, m); err != nil {
			return nil, fmt.Errorf("encoding %s: %w", file, err)
		}
		r.Files[file] = buf.Bytes()
		r.Masks++
		if r.Shell.Masks == nil {
			r.Shell.Masks = map[string]string{}
		}
		r.Shell.Masks[name] = file
		masks[name] = m
	}
	if len(masks) > 0 && masks["silhouette"] == nil {
		return nil, fmt.Errorf("the masks group has no %q layer; it marks the whole ghost, and is required", "silhouette")
	}
	return masks, nil
}

// anyInside reports whether a mask marks any pixel as inside, which is a
// value of 128 or more.
func anyInside(m *image.Gray) bool {
	if m == nil {
		return false
	}
	for _, v := range m.Pix {
		if v >= 128 {
			return true
		}
	}
	return false
}

func sortedNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Keep carries what an author added by hand to the layers of the shell
// being replaced — parent, pivot and region, matched by layer name — onto
// the same layers here. None of them is in the drawing, and a re-import
// that dropped them would undo the rigging every time the art changed.
func (r *Result) Keep(old shell.Manifest) {
	byName := map[string]shell.Layer{}
	for _, l := range old.Layers {
		byName[l.Name] = l
	}
	for i := range r.Shell.Layers {
		l := &r.Shell.Layers[i]
		o, ok := byName[l.Name]
		if !ok {
			continue
		}
		if o.Parent != "" {
			l.Parent = o.Parent
		}
		if o.Pivot != [2]int{} {
			l.Pivot = o.Pivot
		}
		if o.Region != "" {
			l.Region = o.Region
		}
	}
}

func (r *Result) note(format string, args ...any) {
	r.Notes = append(r.Notes, fmt.Sprintf(format, args...))
}

// split decides a layer's name and, if it has one, its variant.
func split(a Art) (layer, variant string) {
	name := a.Name
	if a.Group != "" {
		return tidy(a.Group), tidy(name)
	}
	if before, after, ok := strings.Cut(name, "/"); ok {
		return tidy(before), tidy(after)
	}
	return tidy(name), ""
}

// tidy makes a name safe for a TOML key, a file name and a pose selector.
func tidy(s string) string {
	s = nameSafe.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-")
	return strings.Trim(s, "-")
}

// marks returns the tight bounds of everything drawn on an image, in the
// image's own coordinates, and whether anything was drawn at all.
func marks(img image.Image) (image.Rectangle, bool) {
	b := img.Bounds()
	minX, minY := b.Max.X, b.Max.Y
	maxX, maxY := b.Min.X, b.Min.Y
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, alpha := img.At(x, y).RGBA(); alpha > 0 {
				if x < minX {
					minX = x
				}
				if y < minY {
					minY = y
				}
				if x >= maxX {
					maxX = x + 1
				}
				if y >= maxY {
					maxY = y + 1
				}
			}
		}
	}
	if minX >= maxX || minY >= maxY {
		return image.Rectangle{}, false
	}
	return image.Rect(minX, minY, maxX, maxY), true
}

// cropTo cuts a piece of art down to a rectangle given in canvas
// coordinates. This is the rule damage tracking depends on: a blink cropped
// to the eyes repaints the eyes, while the same blink on a full-canvas
// sheet repaints the whole ghost.
func cropTo(a Art, box image.Rectangle) *image.NRGBA {
	// Where the box falls inside this layer's own image.
	origin := image.Pt(a.X-a.Img.Bounds().Min.X, a.Y-a.Img.Bounds().Min.Y)
	src := box.Sub(origin)
	out := image.NewNRGBA(image.Rect(0, 0, box.Dx(), box.Dy()))
	draw.Draw(out, out.Bounds(), a.Img, src.Min, draw.Src)
	return out
}

// TOML renders the [shell] section. Written by hand rather than with an
// encoder because this is documentation as much as configuration: the
// output is the first shell most authors will read, and it should look like
// something a person wrote.
func (r *Result) TOML() string {
	var b strings.Builder
	b.WriteString("[shell]\n")
	if r.Shell.Scale != 0 {
		fmt.Fprintf(&b, "scale = %s\n", formatScale(r.Shell.Scale))
	}
	if len(r.Shell.Masks) > 0 {
		b.WriteString("\n[shell.masks]\n")
		// The silhouette first: it is the one every shell with masks has,
		// and the outline the others sit in.
		names := []string{"silhouette"}
		for _, name := range sortedNames(r.Shell.Masks) {
			if name != "silhouette" {
				names = append(names, name)
			}
		}
		for _, name := range names {
			if path, ok := r.Shell.Masks[name]; ok {
				fmt.Fprintf(&b, "%s = %q\n", name, path)
			}
		}
	}
	for _, l := range r.Shell.Layers {
		b.WriteString("\n[[shell.layer]]\n")
		fmt.Fprintf(&b, "name = %q\n", l.Name)
		if l.Mask != "" {
			fmt.Fprintf(&b, "mask = %q\n", l.Mask)
		}
		if l.Image != "" {
			fmt.Fprintf(&b, "image = %q\n", l.Image)
		}
		if len(l.Variants) > 0 {
			b.WriteString("variants = { ")
			for i, name := range sortedNames(l.Variants) {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "%s = %q", name, l.Variants[name])
			}
			b.WriteString(" }\n")
			fmt.Fprintf(&b, "default = %q\n", l.Default)
		}
		fmt.Fprintf(&b, "z = %d\n", l.Z)
		if l.At[0] != 0 || l.At[1] != 0 {
			fmt.Fprintf(&b, "at = [%d, %d]\n", l.At[0], l.At[1])
		}
		if l.Pivot[0] != 0 || l.Pivot[1] != 0 {
			fmt.Fprintf(&b, "pivot = [%d, %d]\n", l.Pivot[0], l.Pivot[1])
		}
		if l.Parent != "" {
			fmt.Fprintf(&b, "parent = %q\n", l.Parent)
		}
		if l.Region != "" {
			fmt.Fprintf(&b, "region = %q\n", l.Region)
		}
	}
	return b.String()
}

// formatScale writes a scale as a TOML float. A whole number keeps its
// ".0", so that `scale = 1.0` reads as the float it is rather than as an
// integer that happens to parse.
func formatScale(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}
