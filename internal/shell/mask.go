package shell

import (
	"fmt"
	"image"
	"image/color"
	"maps"
	"math"
	"regexp"
	"slices"
)

// Mask thresholds. A mask pixel is inside at 128 and above, half way, so a
// soft painted edge is split down its middle. Art counts as drawn for
// containment at alpha 32: below that is an antialiased fringe nobody sees,
// and failing a shell for it would make every generated variant an error.
// The canvas edge is stricter (any alpha at all), because a pixel off the
// canvas is not drawn smaller, it is cut off.
const (
	maskInside  = 128
	artContains = 32
)

// maskName is what a mask may be called: it appears in paths and tool
// arguments, so nothing that needs quoting.
var maskName = regexp.MustCompile(`^[a-z0-9-]+$`)

// MaskFromImage reads a mask: each pixel is luminance times alpha, so a
// greyscale PNG and white paint on a transparent layer both work. Inside
// is 128 and above; the soft edge is kept.
//
// The result's origin is (0, 0), which is the canvas's: a mask is a
// picture of the whole canvas, not a patch placed on it.
func MaskFromImage(img image.Image) *image.Gray {
	b := img.Bounds()
	out := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	nrgba, fast := img.(*image.NRGBA)
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			var c color.NRGBA
			if fast {
				c = nrgba.NRGBAAt(b.Min.X+x, b.Min.Y+y)
			} else {
				c = color.NRGBAModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
			}
			lum := 0.299*float64(c.R) + 0.587*float64(c.G) + 0.114*float64(c.B)
			v := math.Round(lum * float64(c.A) / 255)
			out.Pix[y*out.Stride+x] = uint8(min(255, v))
		}
	}
	return out
}

// Tolerance is how far, in art pixels, masked art may stray outside its
// mask on a canvas of w by h: a faint antialiased fringe is not a mistake.
// It grows with the canvas, since the same slip of a brush is more pixels
// on a larger picture, and is never less than 2.
func Tolerance(w, h int) int {
	return max(2, int(math.Round(0.005*float64(max(w, h)))))
}

// CheckContained reports art outside a mask: pixels with alpha of 32 or
// more, placed at at, that fall outside the mask grown by tol. Callers add
// the layer and variant to the error.
//
// The mask is grown square (a pixel within tol along both axes counts as
// near it), and the canvas is the mask's bounds: art beyond them is outside
// whatever the mask says.
func CheckContained(mask *image.Gray, art *image.NRGBA, at image.Point, tol int) error {
	return contained(dilate(mask, tol), art, at)
}

// grown is a mask's inside, dilated, as one flag per canvas pixel.
type grown struct {
	bounds image.Rectangle
	in     []bool
}

// has reports whether a canvas point is inside the grown mask.
func (g grown) has(x, y int) bool {
	if !image.Pt(x, y).In(g.bounds) {
		return false
	}
	return g.in[(y-g.bounds.Min.Y)*g.bounds.Dx()+(x-g.bounds.Min.X)]
}

// dilate grows a mask's inside by tol pixels in every direction. It is
// separable, a row pass then a column pass, each a sliding count, so the
// cost does not depend on tol: masks are canvas-sized, and a 1024-pixel
// canvas is checked once per variant.
func dilate(mask *image.Gray, tol int) grown {
	b := mask.Bounds()
	w, h := b.Dx(), b.Dy()
	tol = max(0, tol)
	rows := make([]bool, w*h)
	for y := 0; y < h; y++ {
		inside := func(x int) bool { return mask.Pix[y*mask.Stride+x] >= maskInside }
		spread(w, tol, inside, func(x int) { rows[y*w+x] = true })
	}
	out := make([]bool, w*h)
	for x := 0; x < w; x++ {
		inside := func(y int) bool { return rows[y*w+x] }
		spread(h, tol, inside, func(y int) { out[y*w+x] = true })
	}
	return grown{bounds: b, in: out}
}

// spread marks every index of a line of n that has an inside index within
// tol of it.
func spread(n, tol int, inside func(int) bool, mark func(int)) {
	count := 0
	// The window for i is [i-tol, i+tol]; prime it with [0, tol).
	for i := 0; i < min(tol, n); i++ {
		if inside(i) {
			count++
		}
	}
	for i := 0; i < n; i++ {
		if j := i + tol; j < n && inside(j) {
			count++
		}
		if j := i - tol - 1; j >= 0 && inside(j) {
			count--
		}
		if count > 0 {
			mark(i)
		}
	}
}

// contained is CheckContained against a mask already grown, which is how
// the loader checks many variants against one mask.
func contained(g grown, art *image.NRGBA, at image.Point) error {
	b := art.Bounds()
	n, box := stray(b.Sub(b.Min).Add(at), func(x, y int) bool {
		p := image.Pt(x, y).Sub(at).Add(b.Min)
		return art.NRGBAAt(p.X, p.Y).A >= artContains && !g.has(x, y)
	})
	if n == 0 {
		return nil
	}
	return fmt.Errorf("%d pixels fall outside the mask, within %v", n, box)
}

// stray counts the points of r for which bad holds, and the smallest
// rectangle holding them all, which is what an author needs to find them.
func stray(r image.Rectangle, bad func(x, y int) bool) (n int, box image.Rectangle) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if !bad(x, y) {
				continue
			}
			n++
			box = box.Union(image.Rect(x, y, x+1, y+1))
		}
	}
	return n, box
}

// loadMasks decodes the declared masks and checks them against each other:
// names, the required silhouette, one size for all (which becomes the
// canvas), nothing empty, and the face masks on the body.
func (s *Shell) loadMasks(dir string, paths map[string]string) error {
	names := slices.Sorted(maps.Keys(paths))
	for _, name := range names {
		if !maskName.MatchString(name) {
			return fmt.Errorf("mask name %q may use only a-z, 0-9 and -", name)
		}
	}
	if _, ok := paths["silhouette"]; !ok {
		return fmt.Errorf("[shell.masks] must declare silhouette")
	}

	s.masks = map[string]*image.Gray{}
	for _, name := range names {
		img, err := loadImage(dir, paths[name])
		if err != nil {
			return fmt.Errorf("mask %q: %w", name, err)
		}
		s.masks[name] = MaskFromImage(img)
	}

	sil := s.masks["silhouette"].Bounds()
	s.artW, s.artH = sil.Dx(), sil.Dy()
	for _, name := range names {
		m := s.masks[name]
		if b := m.Bounds(); b.Dx() != s.artW || b.Dy() != s.artH {
			return fmt.Errorf("mask %q is %dx%d but silhouette is %dx%d; every mask must be the canvas size",
				name, b.Dx(), b.Dy(), s.artW, s.artH)
		}
		if !slices.ContainsFunc(m.Pix, func(v uint8) bool { return v >= maskInside }) {
			return fmt.Errorf("mask %q is empty: nothing in it is 128 or brighter", name)
		}
	}

	// A face is drawn on the body, so its masks lie on the silhouette. A
	// slot need not: a hat sits above the head and a held prop beside it.
	tol := Tolerance(s.artW, s.artH)
	body := dilate(s.masks["silhouette"], tol)
	for _, name := range []string{"eyes", "mouth"} {
		m, ok := s.masks[name]
		if !ok {
			continue
		}
		n, box := stray(m.Bounds(), func(x, y int) bool {
			return m.Pix[y*m.Stride+x] >= maskInside && !body.has(x, y)
		})
		if n > 0 {
			return fmt.Errorf("mask %q reaches outside the silhouette: %d pixels, within %v", name, n, box)
		}
	}
	return nil
}

// checkArt holds every variant of every layer to the canvas, and every
// variant of a masked layer to its mask. It runs at art resolution, before
// anything is scaled, so the check is the one the importer runs.
func (s *Shell) checkArt() error {
	canvas := image.Rect(0, 0, s.artW, s.artH)
	tol := Tolerance(s.artW, s.artH)
	grownMasks := map[string]grown{}
	for _, l := range s.layers {
		at := image.Pt(l.At[0], l.At[1])
		for _, v := range slices.Sorted(maps.Keys(l.variants)) {
			art := l.variants[v]
			b := art.Bounds()
			placed := b.Sub(b.Min).Add(at)
			if !placed.In(canvas) {
				n, box := stray(placed, func(x, y int) bool {
					p := image.Pt(x, y)
					q := p.Sub(at).Add(b.Min)
					return !p.In(canvas) && art.NRGBAAt(q.X, q.Y).A > 0
				})
				if n > 0 {
					return fmt.Errorf("%s draws %d pixels off the %dx%d canvas, within %v",
						describe(l.Name, v), n, s.artW, s.artH, box)
				}
			}
			if l.Mask == "" || b.Empty() {
				continue
			}
			g, ok := grownMasks[l.Mask]
			if !ok {
				g = dilate(s.masks[l.Mask], tol)
				grownMasks[l.Mask] = g
			}
			if err := contained(g, art, at); err != nil {
				return fmt.Errorf("%s strays outside mask %q: %w", describe(l.Name, v), l.Mask, err)
			}
		}
	}
	return nil
}

// describe names a layer's variant for an error; a single-image layer's
// only appearance has no name of its own.
func describe(layer, variant string) string {
	if variant == "" {
		return fmt.Sprintf("shell layer %q", layer)
	}
	return fmt.Sprintf("shell layer %q variant %q", layer, variant)
}
