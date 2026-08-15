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

// TestClipFrame: a clip is a pure function of elapsed time, which is what
// lets the driver schedule one wake-up per keyframe instead of running a
// frame loop.
func TestClipFrame(t *testing.T) {
	blink := Clip{Name: "blink", Keys: []Key{
		{At: 0, Set: map[string]string{"eyes": "closed"}},
		{At: 100, Set: map[string]string{"eyes": "open"}},
		{At: 300, Set: map[string]string{"mouth": "smile"}},
	}}

	for _, tc := range []struct {
		elapsed    int
		wantEyes   string
		wantMouth  string
		wantNextIn int
		wantDone   bool
	}{
		{0, "closed", "", 100, false},
		{50, "closed", "", 50, false},
		{100, "open", "", 200, false},
		{299, "open", "", 1, false},
		// Keyframes accumulate: the smile arrives without reopening the
		// question of the eyes.
		{300, "open", "smile", 0, true},
		{5000, "open", "smile", 0, true},
	} {
		set, nextIn, done := blink.Frame(tc.elapsed)
		if set["eyes"] != tc.wantEyes || set["mouth"] != tc.wantMouth {
			t.Errorf("at %dms: set = %v, want eyes=%q mouth=%q", tc.elapsed, set, tc.wantEyes, tc.wantMouth)
		}
		if nextIn != tc.wantNextIn || done != tc.wantDone {
			t.Errorf("at %dms: nextIn = %d done = %v, want %d/%v", tc.elapsed, nextIn, done, tc.wantNextIn, tc.wantDone)
		}
	}

	if got := blink.Duration(); got != 300 {
		t.Errorf("duration = %d, want 300", got)
	}
	if _, _, done := (Clip{}).Frame(0); !done {
		t.Error("an empty clip is not done")
	}
}

// TestOverridesLayerOverThePose: a clip changes what is showing without
// changing what the ghost *is* — clear the override and the pose is intact.
func TestOverridesLayerOverThePose(t *testing.T) {
	s, _ := testShell(t)
	s.SetPose("asleep") // eyes closed (blue)
	if got := at(t, s, 8, 8); got != blue {
		t.Fatalf("pose did not apply: %v", got)
	}

	s.SetOverrides(map[string]string{"eyes": "open"})
	if got := at(t, s, 8, 8); got != green {
		t.Errorf("override not showing: %v", got)
	}
	if s.Pose() != "asleep" {
		t.Errorf("the override changed the pose to %q", s.Pose())
	}

	s.SetOverrides(nil)
	if got := at(t, s, 8, 8); got != blue {
		t.Errorf("clearing the override did not restore the pose: %v", got)
	}
}

// TestClipValidation: an animation that names something the shell does not
// have is a mistake worth catching at load.
func TestClipValidation(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, dir, "a.png", 10, 10, red)
	base := []Layer{{Name: "eyes", Variants: map[string]string{"open": "a.png"}, Default: "open"}}

	for _, tc := range []struct {
		name string
		clip Clip
		want string
	}{
		{"no name", Clip{Keys: []Key{{At: 0}}}, "no name"},
		{"no keys", Clip{Name: "x"}, "no keys"},
		{"bad trigger", Clip{Name: "x", Trigger: "occasionally", Keys: []Key{{At: 0}}}, "unknown trigger"},
		{"negative time", Clip{Name: "x", Keys: []Key{{At: -5}}}, "negative time"},
		{"out of order", Clip{Name: "x", Keys: []Key{{At: 100}, {At: 50}}}, "out of time order"},
		{"unknown layer", Clip{Name: "x", Keys: []Key{
			{At: 0, Set: map[string]string{"tail": "open"}},
		}}, "unknown layer"},
		{"unknown variant", Clip{Name: "x", Keys: []Key{
			{At: 0, Set: map[string]string{"eyes": "winking"}},
		}}, "unknown variant"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(dir, Manifest{Layers: base, Clips: []Clip{tc.clip}})
			if err == nil || !contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}

	// Duplicate names are caught too.
	c := Clip{Name: "x", Keys: []Key{{At: 0, Set: map[string]string{"eyes": "open"}}}}
	if _, err := Load(dir, Manifest{Layers: base, Clips: []Clip{c, c}}); err == nil ||
		!contains(err.Error(), "declared twice") {
		t.Errorf("err = %v, want one about a duplicate", err)
	}

	// A clip with no trigger is manual: it runs when asked, not on its own.
	s, err := Load(dir, Manifest{Layers: base, Clips: []Clip{c}})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := s.Clip("x")
	if !ok {
		t.Fatal("clip not found")
	}
	if got.Trigger != TriggerManual {
		t.Errorf("trigger = %q, want %q", got.Trigger, TriggerManual)
	}
}

// TestBuiltinBlinks: the placeholder ghost animates, so a ghost with no art
// still looks alive and the demo exercises the real path.
func TestBuiltinBlinks(t *testing.T) {
	s := Builtin(64)
	clip, ok := s.Clip("blink")
	if !ok {
		t.Fatal("the builtin does not blink")
	}
	if clip.Trigger != TriggerSometimes {
		t.Errorf("trigger = %q, want %q", clip.Trigger, TriggerSometimes)
	}
	open := s.Sprite()
	set, _, _ := clip.Frame(0)
	s.SetOverrides(set)
	if s.Sprite() == open {
		t.Error("the blink's first frame looks identical to open eyes")
	}
}

// TestDamageIsLimitedToWhatChanged: a blink must repaint two eyes, not a
// whole ghost. Layer art cropped to the area it covers is what makes that
// possible, which is why the built-in shell is authored that way.
func TestDamageIsLimitedToWhatChanged(t *testing.T) {
	s := Builtin(160)
	full := image.Rect(0, 0, 160, 160)

	if got := s.Damage(State{}); got != full {
		t.Errorf("first paint damage = %v, want the whole canvas", got)
	}

	before := s.State()
	if got := s.Damage(before); !got.Empty() {
		t.Errorf("damage with nothing changed = %v, want empty", got)
	}

	// The blink's closed-eyes frame.
	clip, _ := s.Clip("blink")
	set, _, _ := clip.Frame(0)
	s.SetOverrides(set)
	got := s.Damage(before)
	if got.Empty() {
		t.Fatal("a blink reported no damage at all")
	}
	if area, whole := got.Dx()*got.Dy(), full.Dx()*full.Dy(); area > whole/8 {
		t.Errorf("a blink damaged %v (%d px of %d): the eye art is not cropped",
			got, area, whole)
	}
	// And it covers the eyes, wherever they are.
	if !got.Overlaps(image.Rect(60, 55, 100, 75)) {
		t.Errorf("blink damage %v does not cover the eyes", got)
	}
}

// TestTransformInterpolation: a `move` keyframe is reached gradually, which
// is the whole difference from a frame-swap format — a breath becomes a
// curve instead of eight drawings.
func TestTransformInterpolation(t *testing.T) {
	c := Clip{Name: "breathe", Keys: []Key{
		{At: 0, Move: map[string]Transform{"body": {Offset: [2]float64{0, 0}}}},
		{At: 1000, Move: map[string]Transform{"body": {Offset: [2]float64{0, 10}}}},
	}}
	for _, tc := range []struct {
		elapsed int
		wantY   float64
	}{{0, 0}, {250, 2.5}, {500, 5}, {1000, 10}} {
		_, move, _, _ := c.FrameAt(tc.elapsed)
		if got := move["body"].Offset[1]; got != tc.wantY {
			t.Errorf("at %dms: offset y = %v, want %v", tc.elapsed, got, tc.wantY)
		}
	}

	if !c.Animates() {
		t.Error("a clip with moves says it does not animate")
	}
	swapOnly := Clip{Keys: []Key{{At: 0, Set: map[string]string{"eyes": "closed"}}}}
	if swapOnly.Animates() {
		t.Error("a variant-swap clip claims to animate, and would be sampled needlessly")
	}
}

// TestEasing: step holds, sine smooths, linear is the default.
func TestEasing(t *testing.T) {
	mid := func(easing string) float64 {
		c := Clip{Keys: []Key{
			{At: 0, Move: map[string]Transform{"x": {}}},
			{At: 100, Ease: easing, Move: map[string]Transform{"x": {Offset: [2]float64{100, 0}}}},
		}}
		_, move, _, _ := c.FrameAt(50)
		return move["x"].Offset[0]
	}
	if got := mid(EaseLinear); got != 50 {
		t.Errorf("linear midpoint = %v, want 50", got)
	}
	if got := mid(EaseStep); got != 0 {
		t.Errorf("step midpoint = %v, want it held at 0", got)
	}
	// Sine is symmetric, so its midpoint matches linear's but its quarter
	// points do not.
	if got := mid(EaseSine); got < 49 || got > 51 {
		t.Errorf("sine midpoint = %v, want ~50", got)
	}
}

// TestTransformMovesPixels: an offset actually relocates the art.
func TestTransformMovesPixels(t *testing.T) {
	s, _ := testShell(t)
	if got := at(t, s, 8, 8); got != green {
		t.Fatalf("eyes not where expected: %v", got)
	}
	s.SetMoves(map[string]Transform{"eyes": {Offset: [2]float64{20, 0}}})
	if got := at(t, s, 8, 8); got == green {
		t.Error("the eyes did not move away from their old position")
	}
	if got := at(t, s, 28, 8); got != green {
		t.Errorf("pixel at the new position = %v, want the eyes there", got)
	}
}

// TestParentedTransforms: rotating an upper arm carries the forearm with
// it. This is the reason `parent` was in the format from the start.
func TestParentedTransforms(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, dir, "body.png", 60, 60, red)
	writePNG(t, dir, "arm.png", 10, 10, green)
	writePNG(t, dir, "hand.png", 6, 6, blue)
	s, err := Load(dir, Manifest{
		Layers: []Layer{
			{Name: "body", Image: "body.png"},
			{Name: "arm", Image: "arm.png", Z: 1, At: [2]int{20, 20}},
			{Name: "hand", Image: "hand.png", Z: 2, At: [2]int{30, 20}, Parent: "arm"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	handAt := func() (int, int) {
		img := s.Sprite().Img
		for y := 0; y < 60; y++ {
			for x := 0; x < 60; x++ {
				if img.NRGBAAt(x, y) == blue {
					return x, y
				}
			}
		}
		return -1, -1
	}
	x0, y0 := handAt()
	if x0 < 0 {
		t.Fatal("the hand is not drawn at all")
	}

	// Move only the arm; the hand must follow.
	s.SetMoves(map[string]Transform{"arm": {Offset: [2]float64{0, 12}}})
	x1, y1 := handAt()
	if y1 != y0+12 || x1 != x0 {
		t.Errorf("hand moved to (%d,%d) from (%d,%d); want it carried 12px down", x1, y1, x0, y0)
	}

	// An unparented layer stays put.
	s.SetMoves(map[string]Transform{"body": {Offset: [2]float64{0, 5}}})
	if x2, y2 := handAt(); x2 != x0 || y2 != y0 {
		t.Errorf("hand at (%d,%d) after moving an unrelated layer; want (%d,%d)", x2, y2, x0, y0)
	}
}

// TestOpacity: fading a layer lets what is behind it show through.
func TestOpacity(t *testing.T) {
	s, _ := testShell(t)
	half := 0.5
	s.SetMoves(map[string]Transform{"eyes": {Opacity: &half}})
	got := at(t, s, 8, 8)
	if got == green || got == red {
		t.Errorf("half-faded eyes = %v, want a blend of the two", got)
	}
	if got.G < 60 || got.R < 60 {
		t.Errorf("blend = %v, want roughly half of each", got)
	}
}

// TestTransformDamage: a moved layer must be repainted both where it was
// and where it now is, or it leaves a trail.
func TestTransformDamage(t *testing.T) {
	s, _ := testShell(t)
	before := s.State()
	s.SetMoves(map[string]Transform{"eyes": {Offset: [2]float64{20, 0}}})

	d := s.Damage(before)
	if d.Empty() {
		t.Fatal("moving a layer reported no damage")
	}
	// The eyes are 10x10 at (5,5), moving to (25,5): damage must span both.
	if !d.Overlaps(image.Rect(5, 5, 15, 15)) {
		t.Errorf("damage %v does not cover where the eyes were", d)
	}
	if !d.Overlaps(image.Rect(25, 5, 35, 15)) {
		t.Errorf("damage %v does not cover where the eyes are now", d)
	}
}
