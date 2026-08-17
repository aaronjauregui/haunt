package artimport

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"regexp"
	"sort"
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
}

// nameSafe is what a layer or variant name may contain once tidied: the
// names end up in TOML keys, file names and pose selectors, and a layer
// called "Left Arm (WIP)" should not become three problems.
var nameSafe = regexp.MustCompile(`[^a-z0-9]+`)

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

	// First pass: decide what each piece of art is called and what it
	// covers. Nothing is cropped yet, because a layer's variants have to be
	// cropped together — see below.
	var pieces []piece
	order := []string{}
	seen := map[string]bool{}
	for _, art := range st.Layers {
		layerName, variantName := split(art)
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

		buf := &bytes.Buffer{}
		if err := png.Encode(buf, cropTo(p.art, box[p.layer])); err != nil {
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

	for z, name := range order {
		e := entries[name]
		if e == nil {
			continue
		}
		b := box[name]
		l := shell.Layer{Name: name, Z: z, At: [2]int{b.Min.X, b.Min.Y}}
		switch {
		case len(e.variants) > 0:
			l.Variants = map[string]string{}
			for _, v := range e.variants {
				l.Variants[v.name] = v.file
			}
			l.Default = e.def
			if l.Default == "" {
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
	}
	return res, nil
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
	for _, l := range r.Shell.Layers {
		b.WriteString("\n[[shell.layer]]\n")
		fmt.Fprintf(&b, "name = %q\n", l.Name)
		if l.Image != "" {
			fmt.Fprintf(&b, "image = %q\n", l.Image)
		}
		if len(l.Variants) > 0 {
			names := make([]string, 0, len(l.Variants))
			for name := range l.Variants {
				names = append(names, name)
			}
			sort.Strings(names)
			b.WriteString("variants = { ")
			for i, name := range names {
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
	}
	return b.String()
}
