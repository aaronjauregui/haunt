package shell

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// writePNG lays down a solid rectangle of art for a layer to use.
func writePNG(t *testing.T, dir, name string, w, h int, c color.NRGBA) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

var (
	red   = color.NRGBA{R: 255, A: 255}
	green = color.NRGBA{G: 255, A: 255}
	blue  = color.NRGBA{B: 255, A: 255}
)

// testShell is a body with a two-variant "eyes" layer on top of it.
func testShell(t *testing.T) (*Shell, string) {
	t.Helper()
	dir := t.TempDir()
	writePNG(t, dir, "body.png", 40, 40, red)
	writePNG(t, dir, "open.png", 10, 10, green)
	writePNG(t, dir, "closed.png", 10, 10, blue)

	s, err := Load(dir, Manifest{
		Layers: []Layer{
			{Name: "body", Image: "body.png", Z: 0, Region: "body"},
			{
				Name:     "eyes",
				Variants: map[string]string{"open": "open.png", "closed": "closed.png"},
				Default:  "open",
				Z:        10,
				At:       [2]int{5, 5},
				Region:   "face",
			},
		},
		Poses: map[string]map[string]string{
			"neutral": {"eyes": "open"},
			"asleep":  {"eyes": "closed"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func at(t *testing.T, s *Shell, x, y int) color.NRGBA {
	t.Helper()
	c := s.Sprite().Img.NRGBAAt(x, y)
	return c
}

// TestCompositesLayers: the stack draws in z order, and a pose selects
// which variant of each layer is showing.
func TestCompositesLayers(t *testing.T) {
	s, _ := testShell(t)

	if w, h := s.Size(); w != 40 || h != 40 {
		t.Errorf("size = %dx%d, want 40x40", w, h)
	}
	if got := at(t, s, 1, 1); got != red {
		t.Errorf("body pixel = %v, want red", got)
	}
	if got := at(t, s, 8, 8); got != green {
		t.Errorf("eyes pixel = %v, want the open (green) variant", got)
	}

	if !s.SetPose("asleep") {
		t.Fatal("asleep is not a pose")
	}
	if got := at(t, s, 8, 8); got != blue {
		t.Errorf("after the pose change, eyes = %v, want the closed (blue) variant", got)
	}
	if got := at(t, s, 1, 1); got != red {
		t.Errorf("the pose change disturbed the body: %v", got)
	}
}

// TestPoseCache: a still ghost composites once. This is what keeps an
// unchanging shell free.
func TestPoseCache(t *testing.T) {
	s, _ := testShell(t)
	first := s.Sprite()
	if second := s.Sprite(); first != second {
		t.Error("an unchanged pose was composited twice")
	}
	s.SetPose("asleep")
	if third := s.Sprite(); third == first {
		t.Error("a pose change returned the old composite")
	}
	// Returning to a pose reuses its earlier composite.
	s.SetPose("neutral")
	if back := s.Sprite(); back != first {
		t.Error("returning to a pose recomposited it")
	}
}

// TestPosesResetUnmentionedLayers: a pose describes a whole face, not a
// diff from whatever was showing before, or expressions would depend on
// their order of arrival.
func TestPosesResetUnmentionedLayers(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, dir, "body.png", 20, 20, red)
	writePNG(t, dir, "a.png", 5, 5, green)
	writePNG(t, dir, "b.png", 5, 5, blue)
	s, err := Load(dir, Manifest{
		Layers: []Layer{
			{Name: "body", Image: "body.png"},
			{Name: "eyes", Variants: map[string]string{"a": "a.png", "b": "b.png"}, Default: "a", Z: 1},
			{Name: "mouth", Variants: map[string]string{"a": "a.png", "b": "b.png"}, Default: "a", Z: 2, At: [2]int{10, 10}},
		},
		Poses: map[string]map[string]string{
			"one": {"eyes": "b", "mouth": "b"},
			"two": {"eyes": "b"}, // says nothing about the mouth
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.SetPose("one")
	s.SetPose("two")
	if got := at(t, s, 12, 12); got != green {
		t.Errorf("mouth = %v, want its default (green) restored by the new pose", got)
	}
}

// TestUnknownPose: brains may ask for expressions a shell does not have.
func TestUnknownPose(t *testing.T) {
	s, _ := testShell(t)
	if s.SetPose("ecstatic") {
		t.Error("an undeclared pose was accepted")
	}
	if s.Pose() == "ecstatic" {
		t.Error("a rejected pose was still worn")
	}
}

// TestRegionAt: clicks report the part of the ghost they landed on, topmost
// first, and nothing where the shell is transparent.
func TestRegionAt(t *testing.T) {
	s, _ := testShell(t)
	if got := s.RegionAt(8, 8); got != "face" {
		t.Errorf("region over the eyes = %q, want face", got)
	}
	if got := s.RegionAt(30, 30); got != "body" {
		t.Errorf("region over the body = %q, want body", got)
	}
	if got := s.RegionAt(100, 100); got != "" {
		t.Errorf("region outside the shell = %q, want none", got)
	}
}

// TestSingleImageShell: the simplest possible shell still works, and is
// simply a stack of one.
func TestSingleImageShell(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, dir, "ghost.png", 32, 48, red)
	s, err := Load(dir, Manifest{Sprite: "ghost.png"})
	if err != nil {
		t.Fatal(err)
	}
	if w, h := s.Size(); w != 32 || h != 48 {
		t.Errorf("size = %dx%d, want 32x48", w, h)
	}
	if got := at(t, s, 1, 1); got != red {
		t.Errorf("pixel = %v, want red", got)
	}
	if len(s.Poses()) != 0 {
		t.Errorf("a single-image shell declares poses: %v", s.Poses())
	}
	if s.SetPose("happy") {
		t.Error("a single-image shell accepted a pose")
	}
}

// TestBuiltinHasExpressions: a ghost with no art still changes face, which
// is what lets the demo and the tests exercise the layered path.
func TestBuiltinHasExpressions(t *testing.T) {
	s := Builtin(64)
	if len(s.Poses()) < 3 {
		t.Errorf("builtin poses = %v, want a few", s.Poses())
	}
	neutral := s.Sprite()
	if !s.SetPose("happy") {
		t.Fatal("builtin has no happy pose")
	}
	if s.Sprite() == neutral {
		t.Error("changing expression did not change the composite")
	}
	if got := s.RegionAt(32, 32); got == "" {
		t.Error("the builtin declares no regions")
	}
}

// TestManifestValidation: the format's mistakes are caught at load, with
// messages that name the offending layer.
func TestManifestValidation(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, dir, "a.png", 10, 10, red)

	for _, tc := range []struct {
		name string
		m    Manifest
		want string
	}{
		{"no name", Manifest{Layers: []Layer{{Image: "a.png"}}}, "no name"},
		{"duplicate", Manifest{Layers: []Layer{
			{Name: "x", Image: "a.png"}, {Name: "x", Image: "a.png"},
		}}, "declared twice"},
		{"empty layer", Manifest{Layers: []Layer{{Name: "x"}}}, "no image or variants"},
		{"both", Manifest{Layers: []Layer{
			{Name: "x", Image: "a.png", Variants: map[string]string{"v": "a.png"}},
		}}, "both image and variants"},
		{"no default", Manifest{Layers: []Layer{
			{Name: "x", Variants: map[string]string{"v": "a.png"}},
		}}, "no default"},
		{"bad default", Manifest{Layers: []Layer{
			{Name: "x", Variants: map[string]string{"v": "a.png"}, Default: "nope"},
		}}, "unknown variant"},
		{"missing art", Manifest{Layers: []Layer{{Name: "x", Image: "gone.png"}}}, "gone.png"},
		{"unknown parent", Manifest{Layers: []Layer{
			{Name: "x", Image: "a.png", Parent: "ghost"},
		}}, "unknown parent"},
		{"self parent", Manifest{Layers: []Layer{
			{Name: "x", Image: "a.png", Parent: "x"},
		}}, "its own parent"},
		{"parent cycle", Manifest{Layers: []Layer{
			{Name: "x", Image: "a.png", Parent: "y"},
			{Name: "y", Image: "a.png", Parent: "x"},
		}}, "cycle"},
		{"pose on unknown layer", Manifest{
			Layers: []Layer{{Name: "x", Image: "a.png"}},
			Poses:  map[string]map[string]string{"p": {"nope": "v"}},
		}, "unknown layer"},
		{"pose with unknown variant", Manifest{
			Layers: []Layer{{Name: "x", Variants: map[string]string{"v": "a.png"}, Default: "v"}},
			Poses:  map[string]map[string]string{"p": {"x": "nope"}},
		}, "unknown variant"},
		{"unknown default pose", Manifest{
			Layers:  []Layer{{Name: "x", Image: "a.png"}},
			Default: "nope",
		}, "not a declared pose"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(dir, tc.m)
			if err == nil {
				t.Fatalf("loaded without error, want one mentioning %q", tc.want)
			}
			if !contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
