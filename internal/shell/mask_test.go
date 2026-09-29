package shell

import (
	"image"
	"image/color"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// maskImage is a w by h mask, white inside r and black elsewhere.
func maskImage(w, h int, r image.Rectangle) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetGray(x, y, color.Gray{Y: 255})
		}
	}
	return img
}

// maskedShell writes a 100x100 shell with masks: a body filling the
// silhouette, eyes with two variants, a mouth with one and no default, and
// an empty hat slot whose mask pokes above the silhouette.
func maskedShell(t *testing.T) (Manifest, string) {
	t.Helper()
	dir := t.TempDir()
	writeArt(t, dir, "silhouette.png", maskImage(100, 100, image.Rect(10, 10, 90, 90)))
	writeArt(t, dir, "eyes-mask.png", maskImage(100, 100, image.Rect(30, 30, 70, 50)))
	writeArt(t, dir, "mouth-mask.png", maskImage(100, 100, image.Rect(40, 60, 60, 75)))
	writeArt(t, dir, "hat-mask.png", maskImage(100, 100, image.Rect(20, 0, 80, 20)))
	writeArt(t, dir, "body.png", solid(80, 80, red))
	writeArt(t, dir, "eyes-open.png", solid(40, 10, green))
	writeArt(t, dir, "eyes-shut.png", solid(40, 4, blue))
	writeArt(t, dir, "mouth-smile.png", solid(20, 6, blue))
	return Manifest{
		Masks: map[string]string{
			"silhouette": "silhouette.png",
			"eyes":       "eyes-mask.png",
			"mouth":      "mouth-mask.png",
			"hat":        "hat-mask.png",
		},
		Layers: []Layer{
			{Name: "body", Image: "body.png", At: [2]int{10, 10}, Z: 1, Region: "body"},
			{
				Name: "eyes", Mask: "eyes", Z: 2, At: [2]int{30, 35}, Parent: "body", Region: "face",
				Variants: map[string]string{"open": "eyes-open.png", "shut": "eyes-shut.png"},
				Default:  "open",
			},
			{
				Name: "mouth", Mask: "mouth", Z: 3, At: [2]int{40, 62}, Parent: "body", Region: "face",
				Variants: map[string]string{"smile": "mouth-smile.png"},
			},
			{Name: "hat", Mask: "hat", Z: 4, Parent: "body", Region: "hat"},
		},
		Poses: map[string]map[string]string{
			"smiling":  {"mouth": "smile"},
			"faceless": {"eyes": "none", "mouth": "none"},
		},
		Clips: []Clip{{Name: "vanish", Keys: []Key{
			{At: 0, Set: map[string]string{"eyes": "none"}},
			{At: 100, Set: map[string]string{"eyes": "open"}},
		}}},
	}, dir
}

func mustLoad(t *testing.T, dir string, m Manifest) *Shell {
	t.Helper()
	s, err := Load(dir, m)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func wantError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("loaded without error, want one mentioning %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to mention %q", err, want)
	}
}

func variantsOf(s *Shell, layer string) []string {
	for _, l := range s.Layers() {
		if l.Name == layer {
			return l.Variants
		}
	}
	return nil
}

// TestMaskFromImage: a mask is luminance times alpha, so grey paint and
// white-on-transparent read the same, and colour counts by its brightness.
func TestMaskFromImage(t *testing.T) {
	img := image.NewNRGBA(image.Rect(5, 5, 10, 6))
	img.SetNRGBA(5, 5, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
	img.SetNRGBA(6, 5, color.NRGBA{R: 255, G: 255, B: 255, A: 128})
	img.SetNRGBA(7, 5, color.NRGBA{R: 200, G: 200, B: 200, A: 255})
	img.SetNRGBA(8, 5, color.NRGBA{R: 255, A: 255})
	img.SetNRGBA(9, 5, color.NRGBA{R: 255, G: 255, B: 255, A: 0})
	m := MaskFromImage(img)
	if m.Bounds() != image.Rect(0, 0, 5, 1) {
		t.Errorf("bounds = %v, want the canvas from the origin", m.Bounds())
	}
	want := []uint8{255, 128, 200, 76, 0}
	if !slices.Equal(m.Pix, want) {
		t.Errorf("values = %v, want %v", m.Pix, want)
	}
	// A greyscale image reads as itself.
	g := image.NewGray(image.Rect(0, 0, 2, 1))
	g.Pix = []uint8{127, 128}
	if got := MaskFromImage(g).Pix; !slices.Equal(got, g.Pix) {
		t.Errorf("greyscale mask read as %v", got)
	}
}

// TestTolerance: two pixels, or half a percent of the longer side.
func TestTolerance(t *testing.T) {
	for _, tc := range []struct{ w, h, want int }{
		{100, 100, 2}, {1024, 1024, 5}, {600, 1400, 7}, {10, 10, 2},
	} {
		if got := Tolerance(tc.w, tc.h); got != tc.want {
			t.Errorf("Tolerance(%d, %d) = %d, want %d", tc.w, tc.h, got, tc.want)
		}
	}
}

// TestCheckContained: art may stray exactly the tolerance past its mask,
// and not a pixel more; faint pixels do not count.
func TestCheckContained(t *testing.T) {
	mask := maskImage(50, 50, image.Rect(20, 20, 30, 30))
	art := solid(10, 10, red)
	if err := CheckContained(mask, art, image.Pt(20, 20), 0); err != nil {
		t.Errorf("art on its mask: %v", err)
	}
	// Two pixels right and down: inside a tolerance of 2.
	if err := CheckContained(mask, art, image.Pt(22, 22), 2); err != nil {
		t.Errorf("art at the tolerance: %v", err)
	}
	// Three: one column and one row outside.
	err := CheckContained(mask, art, image.Pt(23, 23), 2)
	if err == nil {
		t.Fatal("art past the tolerance passed")
	}
	if !strings.Contains(err.Error(), "19 pixels") || !strings.Contains(err.Error(), "(23,23)-(33,33)") {
		t.Errorf("error = %q, want the 19 pixels and their bounds", err)
	}
	// A faint fringe is not a mistake.
	faint := solid(10, 10, color.NRGBA{R: 255, A: 31})
	if err := CheckContained(mask, faint, image.Pt(35, 35), 2); err != nil {
		t.Errorf("faint art outside: %v", err)
	}
	// Off the canvas is outside, whatever the mask says.
	full := maskImage(10, 10, image.Rect(0, 0, 10, 10))
	if err := CheckContained(full, solid(4, 4, red), image.Pt(8, 8), 0); err == nil {
		t.Error("art off the canvas passed")
	}
}

// TestMaskedShell: the fixture loads, the canvas is the masks' size, and
// masked layers get the implicit none.
func TestMaskedShell(t *testing.T) {
	m, dir := maskedShell(t)
	s := mustLoad(t, dir, m)
	if w, h := s.Size(); w != 100 || h != 100 {
		t.Errorf("size = %dx%d, want the masks' 100x100", w, h)
	}
	if w, h := s.ArtSize(); w != 100 || h != 100 {
		t.Errorf("art size = %dx%d, want 100x100", w, h)
	}
	if got := s.Masks(); !slices.Equal(got, []string{"eyes", "hat", "mouth", "silhouette"}) {
		t.Errorf("masks = %v", got)
	}
	if s.Mask("eyes") == nil || s.Mask("nose") != nil {
		t.Error("Mask does not find exactly the declared masks")
	}
	if got := variantsOf(s, "eyes"); !slices.Equal(got, []string{"none", "open", "shut"}) {
		t.Errorf("eyes variants = %v, want none added", got)
	}
	if got := variantsOf(s, "body"); !slices.Equal(got, []string{""}) {
		t.Errorf("unmasked body variants = %v, want just its image", got)
	}
	info := s.Layers()
	if names := []string{info[0].Name, info[1].Name, info[2].Name, info[3].Name}; !slices.Equal(names, []string{"body", "eyes", "mouth", "hat"}) {
		t.Errorf("layers = %v, want z order", names)
	}
	if info[2].Default != "none" {
		t.Errorf("mouth with no default defaults to %q, want none", info[2].Default)
	}
	if info[3].Default != "none" || !slices.Equal(info[3].Variants, []string{"none"}) {
		t.Errorf("empty slot = %+v, want only none", info[3])
	}
	if info[1].Mask != "eyes" || info[1].Parent != "body" || info[1].Region != "face" {
		t.Errorf("eyes info = %+v", info[1])
	}
	// The mouth shows nothing at rest, and its pose shows it.
	if got := at(t, s, 45, 63); got != red {
		t.Errorf("mouth at rest = %v, want the body showing through", got)
	}
	s.SetPose("smiling")
	if got := at(t, s, 45, 63); got != blue {
		t.Errorf("smiling mouth = %v, want blue", got)
	}
}

// TestMaskedImageLayer: a masked single-image layer has its image and none,
// and shows its image by default.
func TestMaskedImageLayer(t *testing.T) {
	m, dir := maskedShell(t)
	m.Layers[1] = Layer{Name: "eyes", Mask: "eyes", Image: "eyes-open.png", At: [2]int{30, 35}, Z: 2}
	m.Clips = nil
	s := mustLoad(t, dir, m)
	if got := variantsOf(s, "eyes"); !slices.Equal(got, []string{"", "none"}) {
		t.Errorf("variants = %v, want the image and none", got)
	}
	if got := at(t, s, 31, 36); got != green {
		t.Errorf("eyes = %v, want the image by default", got)
	}
}

// TestDeclaredNoneIsKept: a layer's own "none" is ordinary art.
func TestDeclaredNoneIsKept(t *testing.T) {
	m, dir := maskedShell(t)
	writeArt(t, dir, "dot.png", solid(1, 1, blue))
	m.Layers[1].Variants["none"] = "dot.png"
	s := mustLoad(t, dir, m)
	s.SetPose("faceless")
	if got := at(t, s, 30, 35); got != blue {
		t.Errorf("declared none = %v, want its own art", got)
	}
}

// TestNoneFromPoseAndClip: poses and clips select none like any variant,
// and it draws nothing.
func TestNoneFromPoseAndClip(t *testing.T) {
	m, dir := maskedShell(t)
	s := mustLoad(t, dir, m)
	if got := at(t, s, 31, 36); got != green {
		t.Fatalf("eyes = %v, want open", got)
	}
	if !s.SetPose("faceless") {
		t.Fatal("faceless pose missing")
	}
	if got := at(t, s, 31, 36); got != red {
		t.Errorf("faceless eyes = %v, want the body", got)
	}
	s.SetPose("smiling")
	c, _ := s.Clip("vanish")
	set, _, _ := c.Frame(0)
	s.SetOverrides(set)
	if got := at(t, s, 31, 36); got != red {
		t.Errorf("eyes under the clip = %v, want the body", got)
	}
}

// TestBase: the base is the unmasked layers at their defaults on the art
// canvas, whatever the pose, and a copy the caller may paint on.
func TestBase(t *testing.T) {
	m, dir := maskedShell(t)
	writeArt(t, dir, "tail-a.png", solid(5, 5, blue))
	writeArt(t, dir, "tail-b.png", solid(5, 5, green))
	m.Layers = append(m.Layers, Layer{
		Name: "tail", Variants: map[string]string{"a": "tail-a.png", "b": "tail-b.png"},
		Default: "a", At: [2]int{80, 80}, Z: 5,
	})
	m.Poses["wag"] = map[string]string{"tail": "b"}
	m.Scale = 0.5
	s := mustLoad(t, dir, m)
	s.SetPose("wag")
	base := s.Base()
	if base == nil {
		t.Fatal("no base")
	}
	if base.Bounds() != image.Rect(0, 0, 100, 100) {
		t.Errorf("base = %v, want the art canvas", base.Bounds())
	}
	for _, tc := range []struct {
		x, y int
		want color.NRGBA
		what string
	}{
		{5, 5, color.NRGBA{}, "outside the body"},
		{50, 20, red, "body"},
		{31, 36, red, "where the eyes are"},
		{82, 82, blue, "the tail's default"},
	} {
		if got := base.NRGBAAt(tc.x, tc.y); got != tc.want {
			t.Errorf("%s = %v, want %v", tc.what, got, tc.want)
		}
	}
	base.SetNRGBA(50, 20, green)
	if s.Base().NRGBAAt(50, 20) != red {
		t.Error("painting on the base changed the shell's")
	}
}

// TestWithoutMasks: a shell that declares no masks keeps every old rule.
func TestWithoutMasks(t *testing.T) {
	s, dir := testShell(t)
	if s.Base() != nil {
		t.Error("a shell without masks has a base")
	}
	if len(s.Masks()) != 0 {
		t.Errorf("masks = %v", s.Masks())
	}
	if got := variantsOf(s, "eyes"); slices.Contains(got, "none") {
		t.Errorf("unmasked variants = %v, want no none", got)
	}
	if s.SetPose("none") {
		t.Error("a pose named none exists")
	}
	_, err := Load(dir, Manifest{Layers: []Layer{{Name: "slot"}}})
	wantError(t, err, "no image or variants")
	_, err = Load(dir, Manifest{Layers: []Layer{{Name: "eyes", Image: "open.png", Mask: "eyes"}}})
	wantError(t, err, `layer "eyes" names mask "eyes" but the shell declares no masks`)
	_, err = Load(dir, Manifest{
		Layers: []Layer{{Name: "eyes", Image: "open.png"}},
		Poses:  map[string]map[string]string{"p": {"eyes": "none"}},
	})
	wantError(t, err, "unknown variant")
}

// TestSilhouetteOnlyShell: silhouette is the only mask a shell needs.
func TestSilhouetteOnlyShell(t *testing.T) {
	m, dir := maskedShell(t)
	m.Masks = map[string]string{"silhouette": "silhouette.png"}
	m.Layers = m.Layers[:1]
	m.Poses, m.Clips = nil, nil
	s := mustLoad(t, dir, m)
	if w, h := s.Size(); w != 100 || h != 100 {
		t.Errorf("size = %dx%d, want 100x100", w, h)
	}
	if s.Base() == nil {
		t.Error("no base")
	}
}

// TestMaskErrors: each rule about masks is a load error naming what to fix.
func TestMaskErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(m *Manifest, dir string)
		want string
	}{
		{"no silhouette", func(m *Manifest, dir string) {
			delete(m.Masks, "silhouette")
		}, "must declare silhouette"},
		{"masks on a sprite", func(m *Manifest, dir string) {
			m.Layers, m.Poses, m.Clips = nil, nil, nil
			m.Sprite = "body.png"
		}, "[shell.masks] needs [[shell.layer]]"},
		{"masks on the placeholder", func(m *Manifest, dir string) {
			m.Layers, m.Poses, m.Clips = nil, nil, nil
		}, "[shell.masks] needs [[shell.layer]]"},
		{"bad name", func(m *Manifest, dir string) {
			m.Masks["Big Hat"] = "hat-mask.png"
		}, `mask name "Big Hat"`},
		{"undeclared mask", func(m *Manifest, dir string) {
			m.Layers[3].Mask = "cape"
		}, `names mask "cape"`},
		{"size mismatch", func(m *Manifest, dir string) {
			writeArt(t, dir, "small.png", maskImage(50, 100, image.Rect(0, 0, 50, 20)))
			m.Masks["hat"] = "small.png"
		}, `mask "hat" is 50x100 but silhouette is 100x100`},
		{"empty mask", func(m *Manifest, dir string) {
			writeArt(t, dir, "dim.png", solid(100, 100, color.NRGBA{R: 127, G: 127, B: 127, A: 255}))
			m.Masks["hat"] = "dim.png"
		}, `mask "hat" is empty`},
		{"eyes off the silhouette", func(m *Manifest, dir string) {
			writeArt(t, dir, "high-eyes.png", maskImage(100, 100, image.Rect(30, 2, 70, 20)))
			m.Masks["eyes"] = "high-eyes.png"
			m.Layers[1].At = [2]int{30, 5}
		}, `mask "eyes" reaches outside the silhouette`},
		{"art off the canvas", func(m *Manifest, dir string) {
			m.Layers[0].At = [2]int{30, 10}
		}, `shell layer "body" draws 800 pixels off the 100x100 canvas`},
		{"art outside its mask", func(m *Manifest, dir string) {
			m.Layers[1].At = [2]int{30, 60}
		}, `shell layer "eyes" variant "open" strays outside mask "eyes"`},
		{"missing mask file", func(m *Manifest, dir string) {
			m.Masks["hat"] = "gone.png"
		}, `mask "hat"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, dir := maskedShell(t)
			tc.edit(&m, dir)
			_, err := Load(dir, m)
			wantError(t, err, tc.want)
		})
	}
}

// TestMaskAllowances: what the rules deliberately permit.
func TestMaskAllowances(t *testing.T) {
	t.Run("hat off the silhouette", func(t *testing.T) {
		// The fixture's hat mask already rises above the silhouette.
		m, dir := maskedShell(t)
		writeArt(t, dir, "cap.png", solid(40, 12, blue))
		m.Layers[3].Variants = map[string]string{"cap": "cap.png"}
		m.Layers[3].At = [2]int{25, 0}
		mustLoad(t, dir, m)
	})
	t.Run("transparent margin off the canvas", func(t *testing.T) {
		m, dir := maskedShell(t)
		padded := image.NewNRGBA(image.Rect(0, 0, 120, 120))
		for y := 20; y < 100; y++ {
			for x := 20; x < 100; x++ {
				padded.SetNRGBA(x, y, red)
			}
		}
		writeArt(t, dir, "padded.png", padded)
		m.Layers[0].Image, m.Layers[0].At = "padded.png", [2]int{-10, -10}
		mustLoad(t, dir, m)
	})
	t.Run("fringe within tolerance", func(t *testing.T) {
		m, dir := maskedShell(t)
		m.Layers[1].At = [2]int{32, 42} // eyes-open reaches y 52, the mask 50
		mustLoad(t, dir, m)
	})
}

// TestZeroSizeVariants: the none variant covers nothing, and nothing that
// draws or measures a layer trips over it.
func TestZeroSizeVariants(t *testing.T) {
	m, dir := maskedShell(t)
	m.Layers[3].At = [2]int{95, 95}
	s := mustLoad(t, dir, m)
	empty := image.NewNRGBA(image.Rectangle{})

	t.Run("canvas", func(t *testing.T) {
		if w, h := s.canvas(); w != 90 || h != 90 {
			t.Errorf("canvas = %dx%d, want the art's 90x90, not stretched by the slot", w, h)
		}
	})
	t.Run("Sprite", func(t *testing.T) {
		s.SetPose("faceless")
		s.SetMoves(map[string]Transform{"hat": {Offset: [2]float64{1.5, 0}, Rotate: 10}})
		defer s.SetMoves(nil)
		if got := at(t, s, 31, 36); got != red {
			t.Errorf("pixel = %v, want the body", got)
		}
	})
	t.Run("Damage", func(t *testing.T) {
		s.SetPose("smiling")
		prev := s.State()
		s.SetPose("faceless")
		d := s.Damage(prev)
		// The eyes and the smile went; nothing else changed.
		if want := image.Rect(30, 35, 70, 68); d != want {
			t.Errorf("damage = %v, want the eyes' and the smile's %v", d, want)
		}
		prev = s.State()
		s.SetMoves(map[string]Transform{"hat": {Offset: [2]float64{3, 3}}})
		defer s.SetMoves(nil)
		if d := s.Damage(prev); !d.Empty() {
			t.Errorf("moving an empty slot damaged %v", d)
		}
	})
	t.Run("layerBounds", func(t *testing.T) {
		for _, tr := range []Transform{identity, {Offset: [2]float64{2.5, 1}}, {Rotate: 45, Scale: 2}} {
			if b := layerBounds(empty.Bounds(), [2]int{10, 10}, [2]float64{3, 3}, tr); !b.Empty() {
				t.Errorf("bounds under %+v = %v, want empty", tr, b)
			}
		}
	})
	t.Run("RegionAt", func(t *testing.T) {
		s.SetPose("faceless")
		if got := s.RegionAt(95, 95); got != "" {
			t.Errorf("region at the empty hat = %q, want none", got)
		}
		if got := s.RegionAt(31, 36); got != "body" {
			t.Errorf("region under the missing eyes = %q, want body", got)
		}
	})
	t.Run("drawTransformed", func(t *testing.T) {
		dst := image.NewNRGBA(image.Rect(0, 0, 10, 10))
		drawTransformed(dst, empty, [2]int{2, 2}, [2]float64{1, 1}, Transform{Rotate: 30, Scale: 1.5})
		if slices.ContainsFunc(dst.Pix, func(v uint8) bool { return v != 0 }) {
			t.Error("drawing nothing drew something")
		}
	})
}

// TestScaleLayers: a scaled shell has its art, positions, hits and clip
// offsets in shell pixels, and nothing else changed.
func TestScaleLayers(t *testing.T) {
	m, dir := legacyShell(t)
	m.Scale = 0.5
	s := mustLoad(t, dir, m)
	if s.Scale() != 0.5 {
		t.Errorf("Scale = %g, want 0.5", s.Scale())
	}
	// The glow, 60x66 at the origin, is the widest and tallest layer.
	if w, h := s.ArtSize(); w != 60 || h != 66 {
		t.Errorf("art size = %dx%d, want 60x66", w, h)
	}
	if w, h := s.Size(); w != 30 || h != 33 {
		t.Errorf("size = %dx%d, want 30x33", w, h)
	}
	body := s.layer("body")
	if body.At != [2]int{3, 2} {
		t.Errorf("body at = %v, want floor((6,5)/2) = (3,2)", body.At)
	}
	// Pivot (24,28) from (6,5) is (30,33) on the art canvas: (15,16.5)
	// scaled, and (12,14.5) from the body's new origin.
	if body.pivot != [2]float64{12, 14.5} {
		t.Errorf("body pivot = %v, want (12,14.5)", body.pivot)
	}
	if b := body.variants[""].Bounds(); b.Dx() != 24 || b.Dy() != 29 {
		t.Errorf("body art = %v, want 24x29 (6..54 → 3..27, 5..61 → 2..31)", b.Size())
	}
	// The hit at (10,0) size 20x8 becomes (5,0) size 10x4.
	if h := s.hits[0]; h.At != [2]int{5, 0} || h.Size != [2]int{10, 4} {
		t.Errorf("hit = %+v, want at (5,0) size 10x4", h)
	}
	drift, _ := s.Clip("drift")
	if got := drift.Keys[1].Move["body"].Offset; got != [2]float64{0.2, -0.65} {
		t.Errorf("offset = %v, want halved", got)
	}
	last := drift.Keys[3].Move["body"]
	if last.Rotate != 12 || last.Scale != 0.9 {
		t.Errorf("rotate, scale = %g, %g; want unchanged", last.Rotate, last.Scale)
	}
	fade, _ := s.Clip("fade")
	if o := fade.Keys[1].Move["glow"].Opacity; o == nil || *o != 0.5 {
		t.Error("opacity changed")
	}
	// The caller's manifest still has art-pixel offsets.
	if got := m.Clips[0].Keys[1].Move["body"].Offset; got != [2]float64{0.4, -1.3} {
		t.Errorf("the manifest's offset became %v", got)
	}
}

// TestScaleOddHit: a hit's corner is floored and its far corner ceiled, so
// a scaled rectangle never shrinks off what it covered.
func TestScaleOddHit(t *testing.T) {
	m, dir := legacyShell(t)
	m.Scale = 1.0 / 3
	m.Hits = []Hit{{Region: "hat", At: [2]int{10, 1}, Size: [2]int{5, 5}}, {Region: "off", At: [2]int{3, 3}}}
	s := mustLoad(t, dir, m)
	// 10/3 = 3.33 → 3; 15/3 = 5 → 5; 1/3 → 0; 6/3 = 2 → 2.
	if h := s.hits[0]; h.At != [2]int{3, 0} || h.Size != [2]int{2, 2} {
		t.Errorf("hit = %+v, want at (3,0) size 2x2", h)
	}
	if h := s.hits[1]; h.Size != [2]int{} {
		t.Errorf("a switched-off hit became %+v", h)
	}
}

// TestScaleMaskedCanvas: with masks, the canvas is the art canvas scaled
// and rounded; masks and the base stay at art resolution.
func TestScaleMaskedCanvas(t *testing.T) {
	m, dir := maskedShell(t)
	m.Scale = 0.33
	s := mustLoad(t, dir, m)
	if w, h := s.Size(); w != 33 || h != 33 {
		t.Errorf("size = %dx%d, want 33x33", w, h)
	}
	if w, h := s.ArtSize(); w != 100 || h != 100 {
		t.Errorf("art size = %dx%d, want 100x100", w, h)
	}
	if s.Mask("eyes").Bounds().Dx() != 100 || s.Base().Bounds().Dx() != 100 {
		t.Error("masks or base were scaled")
	}
	if b := s.layer("hat").variants["none"].Bounds(); !b.Empty() {
		t.Errorf("none became %v", b)
	}
	s.Sprite()
}

// TestScaleSprite: the single-image form honours scale too.
func TestScaleSprite(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, dir, "ghost.png", 32, 48, red)
	s, err := LoadWith(dir, Manifest{Sprite: "ghost.png", Scale: 0.5}, LoadOptions{ScaleFactor: 1.5})
	if err != nil {
		t.Fatal(err)
	}
	if w, h := s.Size(); w != 24 || h != 36 {
		t.Errorf("size = %dx%d, want 24x36", w, h)
	}
	if w, h := s.ArtSize(); w != 32 || h != 48 {
		t.Errorf("art size = %dx%d, want 32x48", w, h)
	}
	if got := at(t, s, 12, 18); got != red {
		t.Errorf("pixel = %v, want red", got)
	}
}

// TestPlaceholder: a manifest with neither sprite nor layers is the whole
// built-in ghost at the effective scale, drawn at that size.
func TestPlaceholder(t *testing.T) {
	s := mustLoad(t, "", Manifest{})
	if w, h := s.Size(); w != 160 || h != 160 {
		t.Errorf("size = %dx%d, want 160", w, h)
	}
	if !slices.Contains(s.Poses(), "happy") {
		t.Error("the empty manifest's shell has no expressions")
	}
	if _, ok := s.Clip("blink"); !ok {
		t.Error("the empty manifest's shell does not blink")
	}

	s, err := LoadWith("", Manifest{Scale: 0.5}, LoadOptions{ScaleFactor: 1.5})
	if err != nil {
		t.Fatal(err)
	}
	if w, h := s.Size(); w != 120 || h != 120 {
		t.Errorf("size = %dx%d, want 120", w, h)
	}
	if s.Scale() != 0.75 {
		t.Errorf("Scale = %g, want 0.75", s.Scale())
	}
	s = mustLoad(t, "", Manifest{Scale: 0.01})
	if w, _ := s.Size(); w != 16 {
		t.Errorf("tiny placeholder = %d, want the 16-pixel floor", w)
	}
}

// TestBuiltinOffsetsScale: the placeholder's motion is written for 160
// pixels and scales with it; its drawing is unchanged at 160.
func TestBuiltinOffsetsScale(t *testing.T) {
	for _, tc := range []struct {
		size         int
		poke, breath float64
	}{{160, 4, 2}, {80, 2, 1}, {320, 8, 4}} {
		s := Builtin(tc.size)
		poke, _ := s.Clip("poke")
		breathe, _ := s.Clip("breathe")
		if got := poke.Keys[1].Move["body"].Offset[1]; got != tc.poke {
			t.Errorf("Builtin(%d) poke = %g, want %g", tc.size, got, tc.poke)
		}
		if got := breathe.Keys[1].Move["body"].Offset[1]; got != tc.breath {
			t.Errorf("Builtin(%d) breath = %g, want %g", tc.size, got, tc.breath)
		}
		if got := s.layer("body").pivot; got != [2]float64{float64(tc.size / 2), float64(tc.size / 2)} {
			t.Errorf("Builtin(%d) pivot = %v", tc.size, got)
		}
	}
}

// TestScaleLimits: scale is checked before anything else; the person's
// factor is held to the limit rather than refused.
func TestScaleLimits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		scale  float64
		factor float64
		want   string
	}{
		{"too large", 5, 0, "shell scale 5"},
		{"negative", -1, 0, "shell scale -1"},
		{"NaN", math.NaN(), 0, "shell scale NaN"},
		{"negative factor", 1, -0.5, "scale factor -0.5"},
		{"NaN factor", 1, math.NaN(), "scale factor NaN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadWith("", Manifest{Scale: tc.scale}, LoadOptions{ScaleFactor: tc.factor})
			wantError(t, err, tc.want)
		})
	}
	s, err := LoadWith("", Manifest{Scale: 4}, LoadOptions{ScaleFactor: 2})
	if err != nil {
		t.Fatal(err)
	}
	if s.Scale() != 4 {
		t.Errorf("Scale = %g, want held to 4", s.Scale())
	}
}

// TestPathsStayInside: nothing the shell names may leave the ghost.
func TestPathsStayInside(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(m *Manifest)
	}{
		{"layer image", func(m *Manifest) { m.Layers[0].Image = "../body.png" }},
		{"variant", func(m *Manifest) { m.Layers[1].Variants["open"] = "shell/../../x.png" }},
		{"mask", func(m *Manifest) { m.Masks["hat"] = "../hat.png" }},
		{"absolute", func(m *Manifest) { m.Masks["hat"] = "/etc/hat.png" }},
		{"sprite", func(m *Manifest) { m.Layers, m.Masks, m.Sprite = nil, nil, "../ghost.png" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, dir := maskedShell(t)
			tc.edit(&m)
			_, err := Load(dir, m)
			wantError(t, err, "must be relative and stay inside the ghost directory")
		})
	}
}

// TestLoadTwice: loading does not touch the manifest, so the same one
// loads into the same shell again.
func TestLoadTwice(t *testing.T) {
	m, dir := legacyShell(t)
	m.Scale = 0.5
	before := m.clone()
	a := mustLoad(t, dir, m)
	b := mustLoad(t, dir, m)
	if !reflect.DeepEqual(m, before) {
		t.Error("loading changed the manifest")
	}
	if !reflect.DeepEqual(a.Clips(), b.Clips()) || !reflect.DeepEqual(a.hits, b.hits) {
		t.Error("the second load's clips or hits differ")
	}
	for _, l := range a.layers {
		if other := b.layer(l.Name); other.At != l.At || other.pivot != l.pivot {
			t.Errorf("layer %q placed differently the second time", l.Name)
		}
	}
	if !reflect.DeepEqual(a.Sprite().Img, b.Sprite().Img) {
		t.Error("the two loads draw differently")
	}
}
