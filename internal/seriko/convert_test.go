package seriko

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

// mem is a Source built in the test, so the fixtures are ours and carry no
// licence question of their own.
type mem map[string][]byte

func (m mem) Read(name string) ([]byte, bool) {
	for k, v := range m {
		if strings.EqualFold(k, name) {
			return v, true
		}
	}
	return nil, false
}

// pngOf paints a w×h image: bg everywhere, fg in the middle third, so a
// converted surface has something to be opaque about.
func pngOf(t *testing.T, w, h int, bg, fg color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := bg
			if x > w/3 && x < 2*w/3 && y > h/3 && y < 2*h/3 {
				c = fg
			}
			img.SetNRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

var (
	green = color.NRGBA{R: 0, G: 255, B: 0, A: 255}
	skin  = color.NRGBA{R: 200, G: 160, B: 140, A: 255}
)

func TestConvertSurfacesBecomePoses(t *testing.T) {
	sh := parse(t, `surface0
{
collision0,10,20,40,60,head
collision1,-1,-1,-1,-1,face
}

surface1
{
}
`)
	src := mem{
		"surface0.png": pngOf(t, 60, 90, green, skin),
		"surface1.png": pngOf(t, 60, 90, green, skin),
	}
	res, err := Convert(sh, src, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Shell.Poses) != 2 {
		t.Errorf("%d poses, want one per surface", len(res.Shell.Poses))
	}
	if res.Shell.Default != "s0" {
		t.Errorf("default pose = %q, want s0", res.Shell.Default)
	}
	body := res.Shell.Layers[0]
	if body.Name != bodyLayer || len(body.Variants) != 2 {
		t.Errorf("body layer = %+v", body)
	}
	// Collisions become named rectangles, and a disabled one is left out.
	if len(res.Shell.Hits) != 1 {
		t.Fatalf("%d hits, want 1 (the -1 one is off)", len(res.Shell.Hits))
	}
	h := res.Shell.Hits[0]
	if h.Region != "head" || h.At != [2]int{10, 20} || h.Size != [2]int{30, 40} {
		t.Errorf("hit = %+v", h)
	}
}

// An overlay animation on the resting surface is the blink everyone wants,
// and it must come out as an automatic clip with its own layer.
func TestConvertOverlayAnimation(t *testing.T) {
	sh := parse(t, `surface0
{
0interval,sometimes
0pattern0,101,5,overlay,20,30
0pattern1,102,5,overlay,20,30
0pattern2,-1,10,overlay,20,30
}
`)
	src := mem{
		"surface0.png":   pngOf(t, 60, 90, green, skin),
		"surface101.png": pngOf(t, 12, 8, green, skin),
		"surface102.png": pngOf(t, 12, 8, green, skin),
	}
	res, err := Convert(sh, src, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Shell.Clips) != 1 {
		t.Fatalf("%d clips, want 1", len(res.Shell.Clips))
	}
	c := res.Shell.Clips[0]
	if c.Trigger != "sometimes" {
		t.Errorf("trigger = %q, want sometimes — it is on the resting surface", c.Trigger)
	}
	// Three patterns, the last of which already clears.
	if len(c.Keys) != 3 {
		t.Fatalf("%d keys, want 3: %+v", len(c.Keys), c.Keys)
	}
	// The wait leads its frame rather than trailing it: pattern0 waits 5
	// (50ms) and *then* draws, so the first frame lands at 50ms and each
	// one after it is another wait later. Reading the wait as a duration
	// puts the first frame at 0 and loses the last one's hold entirely.
	if got := []int{c.Keys[0].At, c.Keys[1].At, c.Keys[2].At}; got[0] != 50 || got[1] != 100 || got[2] != 200 {
		t.Errorf("keys at %v ms, want [50 100 200]", got)
	}
	if c.Keys[2].Set[c.Name] != "none" {
		t.Errorf("the clip does not end cleared: %+v", c.Keys[2])
	}

	var layer *struct{ Name, Default string }
	for _, l := range res.Shell.Layers {
		if l.Name == c.Name {
			layer = &struct{ Name, Default string }{l.Name, l.Default}
			if l.At != [2]int{20, 30} {
				t.Errorf("the animation layer sits at %v, want the pattern offset", l.At)
			}
			if len(l.Variants) != 3 {
				t.Errorf("%d variants, want two frames plus nothing", len(l.Variants))
			}
		}
	}
	if layer == nil {
		t.Fatal("no layer for the clip")
	}
	// Invisible until the clip runs, or every animation shows its first
	// frame the moment the ghost appears.
	if layer.Default != "none" {
		t.Errorf("animation layer rests on %q, want none", layer.Default)
	}
}

// A `base` animation swaps the whole body rather than overlaying part of
// it, so it drives the body layer and needs no layer of its own.
func TestConvertBaseAnimationDrivesTheBody(t *testing.T) {
	sh := parse(t, `surface0
{
0interval,runonce
0pattern0,113,10,base
0pattern1,114,10,base
}
`)
	src := mem{
		"surface0.png":   pngOf(t, 60, 90, green, skin),
		"surface113.png": pngOf(t, 60, 90, green, skin),
		"surface114.png": pngOf(t, 60, 90, green, skin),
	}
	res, err := Convert(sh, src, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Shell.Layers) != 1 {
		t.Errorf("%d layers, want only the body", len(res.Shell.Layers))
	}
	c := res.Shell.Clips[0]
	if c.Trigger != "runonce" {
		t.Errorf("trigger = %q", c.Trigger)
	}
	for _, k := range c.Keys {
		if _, ok := k.Set[bodyLayer]; !ok {
			t.Errorf("a base frame did not drive the body: %+v", k)
		}
	}
	// And it puts the body back, or the ghost keeps the last frame forever.
	if got := c.Keys[len(c.Keys)-1].Set[bodyLayer]; got != "s0" {
		t.Errorf("the clip ends on %q, want back on s0", got)
	}
}

// An animation belonging to a surface the ghost does not wear cannot fire
// by itself: its frames are placed for that surface's face.
func TestAnimationsOffTheRestingSurfaceAreManual(t *testing.T) {
	sh := parse(t, `surface0
{
}

surface1
{
0interval,sometimes
0pattern0,111,5,overlay,20,30
0pattern1,-1,5,overlay,20,30
}
`)
	src := mem{
		"surface0.png":   pngOf(t, 60, 90, green, skin),
		"surface1.png":   pngOf(t, 60, 90, green, skin),
		"surface111.png": pngOf(t, 12, 8, green, skin),
	}
	res, err := Convert(sh, src, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Shell.Clips) != 1 {
		t.Fatalf("%d clips", len(res.Shell.Clips))
	}
	if got := res.Shell.Clips[0].Trigger; got != "manual" {
		t.Errorf("trigger = %q, want manual", got)
	}
	if !hasNote(res.Notes, "does not rest on") {
		t.Errorf("the conversion did not explain itself: %v", res.Notes)
	}
}

// -only keeps a two-hundred-surface shell from converting to two hundred
// full-size PNGs, and must drag in the frames its animations reference.
func TestConvertOnlyPullsInAnimationFrames(t *testing.T) {
	sh := parse(t, `surface0
{
0interval,sometimes
0pattern0,101,5,overlay,0,0
0pattern1,-1,5,overlay,0,0
}

surface5
{
}
`)
	src := mem{
		"surface0.png":   pngOf(t, 60, 90, green, skin),
		"surface5.png":   pngOf(t, 60, 90, green, skin),
		"surface101.png": pngOf(t, 12, 8, green, skin),
	}
	res, err := Convert(sh, src, Options{Only: []int{0}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.Shell.Poses["s5"]; ok {
		t.Error("surface 5 was not asked for")
	}
	if len(res.Shell.Clips) != 1 {
		t.Error("the animation on surface 0 should have survived -only")
	}
}

// A shell that does not have the surface it was told to rest on should
// still open. Refusing over a missing surface 0 is an importer nobody uses.
func TestConvertFallsBackToTheLowestSurface(t *testing.T) {
	sh := parse(t, "surface3\n{\n}\n")
	src := mem{"surface3.png": pngOf(t, 60, 90, green, skin)}
	res, err := Convert(sh, src, Options{Default: 0})
	if err != nil {
		t.Fatal(err)
	}
	if res.Shell.Default != "s3" {
		t.Errorf("default = %q, want s3", res.Shell.Default)
	}
	if !hasNote(res.Notes, "resting on surface 3") {
		t.Errorf("the fallback was not explained: %v", res.Notes)
	}
}

func TestConvertNeedsSurfaces(t *testing.T) {
	if _, err := Convert(&Shell{Surfaces: map[int]*Surface{}}, mem{}, Options{}); err == nil {
		t.Error("a shell with no surfaces should be an error")
	}
	sh := parse(t, "surface0\n{\n}\n")
	if _, err := Convert(sh, mem{}, Options{}); err == nil {
		t.Error("a shell whose images are missing should be an error")
	}
}

// Elements composite into one picture, which is the other way a surface is
// described.
func TestConvertComposesElements(t *testing.T) {
	sh := parse(t, `surface0
{
element0,base,body.png,0,0
element1,overlay,hat.png,10,5
}
`)
	src := mem{
		"body.png": pngOf(t, 60, 90, green, skin),
		"hat.png":  pngOf(t, 20, 20, green, skin),
	}
	res, err := Convert(sh, src, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) == 0 {
		t.Fatal("nothing was written")
	}
	img := decodePNG(t, res.Files["shell/surface0.png"])
	if img.Bounds().Dx() != 60 || img.Bounds().Dy() != 90 {
		t.Errorf("composited to %v, want the base's size", img.Bounds())
	}
}

func hasNote(notes []string, want string) bool {
	for _, n := range notes {
		if strings.Contains(n, want) {
			return true
		}
	}
	return false
}

func decodePNG(t *testing.T, b []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// Both formats have `sometimes` and `rarely` and they mean nearly opposite
// things — SERIKO's are probabilities per second, haunt's are interval
// ranges. Matching on the word turns a four-second blink into a
// ninety-second one, which is the difference between a ghost that looks
// alive and one that looks broken.
func TestIntervalsMapByPeriodNotByName(t *testing.T) {
	for _, tc := range []struct {
		interval string
		every    int
		want     string
	}{
		// One-in-two per second: about every 2s.
		{"sometimes", 0, "sometimes"},
		// One-in-four per second: about every 4s — nowhere near haunt's
		// `rarely`, despite sharing its name.
		{"rarely", 0, "sometimes"},
		{"periodic", 3, "sometimes"},
		{"random", 10, "sometimes"},
		// Genuinely occasional, so the slow trigger is right.
		{"random", 120, "rarely"},
		{"periodic", 60, "rarely"},
	} {
		a := &Animation{Interval: tc.interval, Every: tc.every}
		got, _ := triggerFor(a, true, "probe")
		if got != tc.want {
			t.Errorf("%s,%d -> %q, want %q", tc.interval, tc.every, got, tc.want)
		}
	}
}

// The non-random intervals still map straight across.
func TestDirectIntervalMappings(t *testing.T) {
	for interval, want := range map[string]string{
		"always":  "always",
		"runonce": "runonce",
		"talk":    "talk",
		"never":   "manual",
		"":        "manual",
		"bind":    "manual",
	} {
		got, _ := triggerFor(&Animation{Interval: interval}, true, "probe")
		if got != want {
			t.Errorf("%q -> %q, want %q", interval, got, want)
		}
	}
}
