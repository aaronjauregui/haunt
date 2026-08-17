package artimport

import (
	"archive/zip"
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aaronjauregui/haunt/internal/shell"
)

// oraLayerSpec is one layer to put in a test drawing.
type oraLayerSpec struct {
	name    string
	group   string
	x, y    int
	hidden  bool
	img     image.Image
	skipXML bool // in the archive but not in stack.xml
}

// writeORA builds a real OpenRaster file: a zip, a stack.xml, and a PNG per
// layer. Tests read what the artist's tool would have written rather than a
// hand-made struct, since the parsing is half the job.
func writeORA(t *testing.T, path string, w, h int, layers []oraLayerSpec) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)

	mt, err := zw.Create("mimetype")
	if err != nil {
		t.Fatal(err)
	}
	mt.Write([]byte("image/openraster"))

	// stack.xml lists layers top first, which is what the real format does
	// and the reverse of drawing order — so the tests are exercising that
	// flip rather than agreeing with the code about it.
	var xml strings.Builder
	xml.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	xml.WriteString(`<image w="` + itoa(w) + `" h="` + itoa(h) + `"><stack>` + "\n")
	group := ""
	for i := len(layers) - 1; i >= 0; i-- {
		l := layers[i]
		if l.group != group {
			if group != "" {
				xml.WriteString("</stack>\n")
			}
			if l.group != "" {
				xml.WriteString(`<stack name="` + l.group + `">` + "\n")
			}
			group = l.group
		}
		src := "data/layer" + itoa(i) + ".png"
		vis := "visible"
		if l.hidden {
			vis = "hidden"
		}
		if !l.skipXML {
			xml.WriteString(`<layer name="` + l.name + `" src="` + src + `" x="` + itoa(l.x) +
				`" y="` + itoa(l.y) + `" visibility="` + vis + `"/>` + "\n")
		}
		w, err := zw.Create(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(w, l.img); err != nil {
			t.Fatal(err)
		}
	}
	if group != "" {
		xml.WriteString("</stack>\n")
	}
	xml.WriteString("</stack></image>\n")

	sx, err := zw.Create("stack.xml")
	if err != nil {
		t.Fatal(err)
	}
	sx.Write([]byte(xml.String()))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

// filled returns an image with an opaque rectangle inside a transparent
// canvas — the shape of art an artist's export actually has.
func filled(w, h int, r image.Rectangle, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

var (
	blue  = color.NRGBA{40, 60, 200, 255}
	white = color.NRGBA{255, 255, 255, 255}
	black = color.NRGBA{0, 0, 0, 255}
)

// The whole point, in one test: a drawing with a body and an eyes group
// becomes a layer with one appearance and a layer with two variants, in
// drawing order, cropped.
func TestConvertGroupsBecomeVariants(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ghost.ora")
	writeORA(t, path, 100, 100, []oraLayerSpec{
		{name: "body", img: filled(100, 100, image.Rect(10, 20, 60, 90), blue)},
		{name: "open", group: "eyes", img: filled(100, 100, image.Rect(30, 30, 46, 38), white)},
		{name: "closed", group: "eyes", hidden: true, img: filled(100, 100, image.Rect(30, 32, 46, 36), black)},
	})

	st, err := ReadORA(path)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Convert(st, "shell")
	if err != nil {
		t.Fatal(err)
	}

	if len(res.Shell.Layers) != 2 {
		t.Fatalf("layers = %+v, want body and eyes", res.Shell.Layers)
	}
	body, eyes := res.Shell.Layers[0], res.Shell.Layers[1]
	if body.Name != "body" || body.Image != "shell/body.png" {
		t.Errorf("body layer = %+v", body)
	}
	if body.Z >= eyes.Z {
		t.Errorf("drawing order was not kept: body z=%d, eyes z=%d", body.Z, eyes.Z)
	}
	if len(eyes.Variants) != 2 || eyes.Variants["open"] == "" || eyes.Variants["closed"] == "" {
		t.Fatalf("eyes variants = %+v, want open and closed", eyes.Variants)
	}
	// The visible one is what the ghost wears: that is what visibility in
	// the drawing means.
	if eyes.Default != "open" {
		t.Errorf("default variant = %q, want the visible one", eyes.Default)
	}
	for _, want := range []string{"shell/body.png", "shell/eyes-open.png", "shell/eyes-closed.png"} {
		if _, ok := res.Files[want]; !ok {
			t.Errorf("no file written for %s (have %v)", want, keys(res.Files))
		}
	}
}

// Cropping is not tidiness: damage tracking repaints the bounds of what
// changed, so a blink drawn on a full-canvas sheet repaints the whole
// ghost. The importer does it so the artist never has to.
func TestLayersAreCroppedToTheirArt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ghost.ora")
	// Drawn at (5,5) on the canvas, with the marks themselves starting 30
	// pixels further in: both offsets have to end up in `at`.
	writeORA(t, path, 200, 200, []oraLayerSpec{
		{name: "body", x: 5, y: 5, img: filled(200, 200, image.Rect(30, 40, 90, 120), blue)},
	})
	st, err := ReadORA(path)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Convert(st, "shell")
	if err != nil {
		t.Fatal(err)
	}

	img, err := png.Decode(bytes.NewReader(res.Files["shell/body.png"]))
	if err != nil {
		t.Fatal(err)
	}
	if got := img.Bounds().Size(); got.X != 60 || got.Y != 80 {
		t.Errorf("cropped size = %v, want the 60x80 the artist drew", got)
	}
	if at := res.Shell.Layers[0].At; at != [2]int{35, 45} {
		t.Errorf("at = %v, want the crop folded into the layer offset", at)
	}
}

// Variants are swapped in place by a pose, so they must share an origin.
// Cropping each to its own marks — which is the obvious thing to do, and
// what the first version did — shifts a closed eye up to where the open
// eye's topmost pixel was. Caught by running the importer for real and
// looking at the ghost.
func TestVariantsShareAnOriginAndSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ghost.ora")
	// The open eye is tall; the closed eye is a line across its middle. On
	// the canvas they must stay where they were drawn.
	writeORA(t, path, 80, 80, []oraLayerSpec{
		{name: "body", img: filled(80, 80, image.Rect(0, 0, 80, 80), blue)},
		{name: "open", group: "eyes", img: filled(80, 80, image.Rect(20, 20, 40, 40), white)},
		{name: "closed", group: "eyes", hidden: true, img: filled(80, 80, image.Rect(20, 29, 40, 31), black)},
	})
	st, err := ReadORA(path)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Convert(st, "shell")
	if err != nil {
		t.Fatal(err)
	}

	open, err := png.Decode(bytes.NewReader(res.Files["shell/eyes-open.png"]))
	if err != nil {
		t.Fatal(err)
	}
	closed, err := png.Decode(bytes.NewReader(res.Files["shell/eyes-closed.png"]))
	if err != nil {
		t.Fatal(err)
	}
	if open.Bounds() != closed.Bounds() {
		t.Fatalf("variants differ in size: open %v, closed %v", open.Bounds(), closed.Bounds())
	}

	var eyes shell.Layer
	for _, l := range res.Shell.Layers {
		if l.Name == "eyes" {
			eyes = l
		}
	}
	if eyes.At != [2]int{20, 20} {
		t.Fatalf("eyes at = %v, want the union origin (20,20)", eyes.At)
	}
	// The closed line lands on canvas row 29-30, where it was drawn, rather
	// than at the top of the variant's own art.
	for y := 0; y < closed.Bounds().Dy(); y++ {
		_, _, _, a := closed.At(2, y).RGBA()
		if a > 0 {
			if canvasY := eyes.At[1] + y; canvasY != 29 {
				t.Errorf("the closed eye starts on canvas row %d, want 29", canvasY)
			}
			break
		}
	}
}

// A hidden top-level layer is a sketch, a ruler, a reference photo. A
// hidden layer inside a group is a variant that is not currently worn.
func TestHiddenTopLevelLayersAreLeftOut(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ghost.ora")
	writeORA(t, path, 60, 60, []oraLayerSpec{
		{name: "sketch", hidden: true, img: filled(60, 60, image.Rect(0, 0, 60, 60), black)},
		{name: "body", img: filled(60, 60, image.Rect(10, 10, 40, 40), blue)},
	})
	st, err := ReadORA(path)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Convert(st, "shell")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Shell.Layers) != 1 || res.Shell.Layers[0].Name != "body" {
		t.Fatalf("layers = %+v, want only the body", res.Shell.Layers)
	}
	if !strings.Contains(strings.Join(res.Notes, "\n"), "sketch") {
		t.Errorf("the artist was not told the sketch was left out: %v", res.Notes)
	}
}

// Names become TOML keys, file names and pose selectors, so "Left Arm
// (WIP)" has to be tidied — and the artist told, because the name they type
// in a pose has to match.
func TestNamesAreTidiedAndReported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ghost.ora")
	writeORA(t, path, 40, 40, []oraLayerSpec{
		{name: "Left Arm (WIP)", img: filled(40, 40, image.Rect(1, 1, 20, 20), blue)},
	})
	st, err := ReadORA(path)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Convert(st, "shell")
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Shell.Layers[0].Name; got != "left-arm-wip" {
		t.Errorf("name = %q", got)
	}
	if !strings.Contains(strings.Join(res.Notes, "\n"), "renamed") {
		t.Errorf("the rename went unmentioned: %v", res.Notes)
	}
}

// The slash convention, for tools and habits that do not use groups.
func TestSlashNamesBecomeVariants(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ghost.ora")
	writeORA(t, path, 40, 40, []oraLayerSpec{
		{name: "mouth/smile", img: filled(40, 40, image.Rect(5, 5, 15, 10), black)},
		{name: "mouth/frown", hidden: true, img: filled(40, 40, image.Rect(5, 5, 15, 10), black)},
	})
	st, err := ReadORA(path)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Convert(st, "shell")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Shell.Layers) != 1 {
		t.Fatalf("layers = %+v, want one", res.Shell.Layers)
	}
	if l := res.Shell.Layers[0]; l.Name != "mouth" || len(l.Variants) != 2 || l.Default != "smile" {
		t.Errorf("layer = %+v", l)
	}
}

// The end of the line: what the importer writes must load as a shell and
// composite, or the whole exercise is a file-format conversion with no
// destination.
func TestTheResultLoadsAsAShell(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ghost.ora")
	writeORA(t, path, 100, 100, []oraLayerSpec{
		{name: "body", img: filled(100, 100, image.Rect(10, 20, 60, 90), blue)},
		{name: "open", group: "eyes", img: filled(100, 100, image.Rect(30, 30, 46, 38), white)},
		{name: "closed", group: "eyes", hidden: true, img: filled(100, 100, image.Rect(30, 32, 46, 36), black)},
	})
	st, err := ReadORA(path)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Convert(st, "shell")
	if err != nil {
		t.Fatal(err)
	}
	ghostDir := t.TempDir()
	for rel, data := range res.Files {
		full := filepath.Join(ghostDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	m := res.Shell
	// A pose, added the way an author would: the importer describes the
	// body, the author says what to do with it.
	m.Poses = map[string]map[string]string{"blink": {"eyes": "closed"}}
	sh, err := shell.Load(ghostDir, m)
	if err != nil {
		t.Fatalf("the generated shell does not load: %v", err)
	}
	w, h := sh.Size()
	if w == 0 || h == 0 {
		t.Fatalf("shell has no size")
	}
	before := sh.Sprite()
	if !sh.SetPose("blink") {
		t.Fatal("the blink pose was not accepted")
	}
	if sh.Sprite() == before {
		t.Error("changing the pose changed nothing on screen")
	}
}

// The TOML is the first shell most authors will read, so it has to be
// something a person could have written — and it has to parse.
func TestTOMLRendersTheLayers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ghost.ora")
	writeORA(t, path, 60, 60, []oraLayerSpec{
		{name: "body", img: filled(60, 60, image.Rect(2, 3, 40, 50), blue)},
		{name: "open", group: "eyes", img: filled(60, 60, image.Rect(10, 10, 20, 14), white)},
	})
	st, err := ReadORA(path)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Convert(st, "shell")
	if err != nil {
		t.Fatal(err)
	}
	out := res.TOML()
	for _, want := range []string{
		"[shell]", "[[shell.layer]]", `name = "body"`, `image = "shell/body.png"`,
		`open = "shell/eyes-open.png"`, `default = "open"`, "at = [2, 3]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("TOML missing %q:\n%s", want, out)
		}
	}
}

func TestEmptyDrawingIsAnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.ora")
	writeORA(t, path, 10, 10, []oraLayerSpec{
		{name: "nothing", img: image.NewNRGBA(image.Rect(0, 0, 10, 10))},
	})
	st, err := ReadORA(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Convert(st, "shell"); err == nil {
		t.Error("a drawing with nothing on it converted successfully")
	}
}

func TestNotAnORAFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadORA(path); err == nil {
		t.Error("a text file was read as OpenRaster")
	}
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
