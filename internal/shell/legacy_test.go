package shell

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// update rewrites the goldens under testdata/legacy from the current code.
// They record what a shell that uses none of the optional fields looks
// like, so a change to the loader or the compositor that moves a pixel of
// an existing shell shows up as a failure rather than as a ghost that looks
// slightly different.
var update = flag.Bool("update", false, "rewrite the golden images in testdata/legacy")

// writeArt saves a decoded image as a PNG under dir.
func writeArt(t *testing.T, dir, name string, img image.Image) {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// disc is a soft-edged disc of colour c: opaque inside, fading over the
// last pixel and a half, so resampling and blending both have edges to get
// wrong.
func disc(w, h int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	cx, cy := float64(w)/2, float64(h)/2
	r := float64(min(w, h)) / 2
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)+0.5-cx, float64(y)+0.5-cy
			d := r - math.Sqrt(dx*dx+dy*dy)
			if d <= 0 {
				continue
			}
			a := 1.0
			if d < 1.5 {
				a = d / 1.5
			}
			// A gradient across the disc, so a misplaced sample shows.
			px := c
			px.R = uint8(int(c.R) * (w - x/2) / w)
			px.A = uint8(float64(c.A) * a)
			img.SetNRGBA(x, y, px)
		}
	}
	return img
}

// legacyShell is a shell that uses every field a shell could use before
// masks and scale: layers at offsets, variants, poses, a hit, a parent and
// pivot, and clips that move layers by fractions of a pixel and by whole
// pixels, rotate and scale them, and fade them.
func legacyShell(t *testing.T) (Manifest, string) {
	t.Helper()
	dir := t.TempDir()
	writeArt(t, dir, "body.png", disc(48, 56, color.NRGBA{R: 230, G: 235, B: 250, A: 255}))
	writeArt(t, dir, "eyes-open.png", disc(18, 8, color.NRGBA{R: 40, G: 60, B: 70, A: 255}))
	writeArt(t, dir, "eyes-shut.png", disc(18, 3, color.NRGBA{R: 40, G: 60, B: 70, A: 255}))
	writeArt(t, dir, "mouth-flat.png", disc(12, 3, color.NRGBA{R: 120, G: 30, B: 30, A: 200}))
	writeArt(t, dir, "mouth-open.png", disc(10, 7, color.NRGBA{R: 120, G: 30, B: 30, A: 255}))
	writeArt(t, dir, "glow.png", disc(60, 66, color.NRGBA{R: 80, G: 220, B: 210, A: 110}))
	return Manifest{
		Layers: []Layer{
			{Name: "glow", Image: "glow.png", Z: 0, At: [2]int{0, 0}, Parent: "body"},
			{Name: "body", Image: "body.png", Z: 1, At: [2]int{6, 5}, Pivot: [2]int{24, 28}, Region: "body"},
			{
				Name:     "eyes",
				Variants: map[string]string{"open": "eyes-open.png", "shut": "eyes-shut.png"},
				Default:  "open", Z: 2, At: [2]int{21, 22}, Parent: "body", Region: "face",
			},
			{
				Name:     "mouth",
				Variants: map[string]string{"flat": "mouth-flat.png", "open": "mouth-open.png"},
				Default:  "flat", Z: 3, At: [2]int{24, 36}, Parent: "body", Region: "face",
			},
		},
		Poses: map[string]map[string]string{
			"neutral": {},
			"asleep":  {"eyes": "shut"},
			"talking": {"mouth": "open"},
		},
		Default: "neutral",
		Hits:    []Hit{{Region: "hat", At: [2]int{10, 0}, Size: [2]int{20, 8}}},
		Clips: []Clip{
			{Name: "drift", Trigger: TriggerAlways, Keys: []Key{
				{At: 0, Move: map[string]Transform{"body": {}}},
				{At: 100, Move: map[string]Transform{"body": {Offset: [2]float64{0.4, -1.3}}}},
				{At: 200, Move: map[string]Transform{"body": {Offset: [2]float64{2, -3}}}},
				{At: 300, Ease: EaseSine, Move: map[string]Transform{"body": {Rotate: 12, Scale: 0.9}}},
			}},
			{Name: "fade", Trigger: TriggerManual, Keys: []Key{
				{At: 0, Move: map[string]Transform{"glow": {}}},
				{At: 100, Move: map[string]Transform{"glow": {Opacity: ptr(0.5)}}},
			}},
		},
	}, dir
}

func ptr(v float64) *float64 { return &v }

// TestLegacyGolden: a shell that uses no optional field composites exactly
// as it always has, pose by pose and frame by frame. Run with -update only
// when a change to the pictures is intended.
func TestLegacyGolden(t *testing.T) {
	m, dir := legacyShell(t)
	s, err := Load(dir, m)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.RegionAt(15, 3); got != "hat" {
		t.Errorf("RegionAt(hit) = %q, want hat", got)
	}

	frames := map[string]*image.NRGBA{}
	for _, pose := range []string{"neutral", "asleep", "talking"} {
		if !s.SetPose(pose) {
			t.Fatalf("pose %q missing", pose)
		}
		frames["pose-"+pose] = s.Sprite().Img
	}
	s.SetPose("neutral")
	for _, clip := range []string{"drift", "fade"} {
		c, ok := s.Clip(clip)
		if !ok {
			t.Fatalf("clip %q missing", clip)
		}
		for _, at := range []int{50, 100, 150, 200, 250, 300} {
			if at > c.Duration() {
				continue
			}
			set, move, _, _ := c.FrameAt(at)
			s.SetOverrides(set)
			s.SetMoves(move)
			frames[fmt.Sprintf("%s-%03d", clip, at)] = s.Sprite().Img
		}
		s.SetOverrides(nil)
		s.SetMoves(nil)
	}

	golden := filepath.Join("testdata", "legacy")
	if *update {
		if err := os.MkdirAll(golden, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, img := range frames {
			writeArt(t, golden, name+".png", img)
		}
		return
	}
	for name, img := range frames {
		f, err := os.Open(filepath.Join(golden, name+".png"))
		if err != nil {
			t.Errorf("%s: %v (run with -update to create it)", name, err)
			continue
		}
		want, err := png.Decode(f)
		f.Close()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !want.Bounds().Eq(img.Bounds()) {
			t.Errorf("%s: size %v, golden %v", name, img.Bounds(), want.Bounds())
			continue
		}
		diffs := 0
		for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
			for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
				if color.NRGBAModel.Convert(want.At(x, y)) != img.NRGBAAt(x, y) {
					diffs++
				}
			}
		}
		if diffs > 0 {
			t.Errorf("%s: %d pixels differ from the golden", name, diffs)
		}
	}
}
