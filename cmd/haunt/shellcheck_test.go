package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aaronjauregui/haunt/internal/ghost"
	"github.com/aaronjauregui/haunt/internal/shell"
)

// checkPNG writes a w by h PNG that is c inside r and transparent outside.
func checkPNG(t *testing.T, path string, w, h int, r image.Rectangle, c color.NRGBA) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

var (
	checkBody   = color.NRGBA{40, 60, 200, 255}
	checkOpen   = color.NRGBA{255, 255, 255, 255}
	checkClosed = color.NRGBA{0, 0, 0, 255}
	checkWhite  = color.NRGBA{255, 255, 255, 255}
)

// checkGhost writes a masked shell on a 20 px canvas: a body with no face,
// and eyes held to an eyes mask, open by default and closed in the pose
// the ghost boots in. eyesMask is the eyes mask's size, which a caller
// makes wrong to break the shell.
func checkGhost(t *testing.T, eyesMask int) string {
	t.Helper()
	dir := t.TempDir()
	art := func(name string) string { return filepath.Join(dir, "shell", name) }
	checkPNG(t, art("silhouette.png"), 20, 20, image.Rect(2, 2, 18, 18), checkWhite)
	checkPNG(t, art("eyes-mask.png"), eyesMask, eyesMask, image.Rect(6, 6, 14, 10), checkWhite)
	checkPNG(t, art("body.png"), 12, 12, image.Rect(0, 0, 12, 12), checkBody)
	checkPNG(t, art("open.png"), 6, 2, image.Rect(0, 0, 6, 2), checkOpen)
	checkPNG(t, art("closed.png"), 6, 2, image.Rect(0, 1, 6, 2), checkClosed)
	manifest := `[ghost]
name = "wisp"
version = "0.1.0"
capabilities = ["message", "llm"]

[shell]
default_pose = "sleepy"

[shell.masks]
silhouette = "shell/silhouette.png"
eyes = "shell/eyes-mask.png"

[[shell.layer]]
name = "body"
image = "shell/body.png"
at = [4, 4]
region = "body"

[[shell.layer]]
name = "eyes"
mask = "eyes"
z = 1
at = [7, 7]
default = "open"
variants = { open = "shell/open.png", closed = "shell/closed.png" }

[shell.pose.sleepy]
eyes = "closed"

[shell.pose.awake]

[[shell.clip]]
name = "blink"
trigger = "sometimes"
keys = [{ at = 0, set = { eyes = "closed" } }, { at = 90 }]
`
	if err := os.WriteFile(filepath.Join(dir, "ghost.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// runShellCheck runs the command in process, with an empty config
// directory unless the test has set its own.
func runShellCheck(t *testing.T, args ...string) (string, error) {
	t.Helper()
	if os.Getenv("HAUNT_CONFIG_DIR") == "" {
		t.Setenv("HAUNT_CONFIG_DIR", t.TempDir())
	}
	var out bytes.Buffer
	err := shellCheck(args, &out)
	return out.String(), err
}

func TestShellCheckReportsAGoodShell(t *testing.T) {
	dir := checkGhost(t, 20)
	out, err := runShellCheck(t, "-C", dir)
	if err != nil {
		t.Fatalf("a good shell failed the check: %v\n%s", err, out)
	}
	for _, want := range []string{
		"scale: 1 (shell) x 1 (factor) = 1 (effective)",
		"canvas: 20x20 art pixels, 20x20 display pixels",
		"body: mask -, region body, parent -, default (image), variants (image)",
		"eyes: mask eyes, region -, parent -, default open, variants closed none open",
		"poses: awake sleepy",
		"blink: trigger sometimes",
		"silhouette: 256 pixels inside",
		"eyes: 32 pixels inside",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
}

// A broken shell fails with the loader's own message, which is the one the
// driver would stop on; main turns the error into exit status 1.
func TestShellCheckFailsWithTheLoadersMessage(t *testing.T) {
	dir := checkGhost(t, 10)
	_, err := runShellCheck(t, "-C", dir)
	if err == nil {
		t.Fatal("a mask of the wrong size passed the check")
	}
	m, merr := ghost.LoadManifest(dir)
	if merr != nil {
		t.Fatal(merr)
	}
	_, lerr := shell.LoadWith(dir, m.Shell, shell.LoadOptions{})
	if lerr == nil {
		t.Fatal("the loader accepts a mask of the wrong size")
	}
	if !strings.Contains(err.Error(), lerr.Error()) {
		t.Errorf("error %q does not carry the loader's %q", err, lerr)
	}
}

// The person's factor comes from their config, keyed by the ghost's name,
// and -factor stands in for it without reading the config at all.
func TestShellCheckAppliesTheConfigFactor(t *testing.T) {
	dir := checkGhost(t, 20)
	cfg := t.TempDir()
	t.Setenv("HAUNT_CONFIG_DIR", cfg)
	toml := "[appearance.ghost.wisp]\nscale = 2\n"
	if err := os.WriteFile(filepath.Join(cfg, "config.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runShellCheck(t, "-C", dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"x 2 (factor) = 2 (effective)", "20x20 art pixels, 40x40 display pixels"} {
		if !strings.Contains(out, want) {
			t.Errorf("with the config's factor, report lacks %q:\n%s", want, out)
		}
	}

	// A config that does not load must not matter once -factor is given.
	if err := os.WriteFile(filepath.Join(cfg, "config.toml"), []byte("[appearance\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runShellCheck(t, "-C", dir); err == nil {
		t.Error("a broken config passed without -factor")
	}
	out, err = runShellCheck(t, "-C", dir, "-factor", "0.5")
	if err != nil {
		t.Fatalf("-factor still read the config: %v", err)
	}
	for _, want := range []string{"x 0.5 (factor) = 0.5 (effective)", "20x20 art pixels, 10x10 display pixels"} {
		if !strings.Contains(out, want) {
			t.Errorf("with -factor, report lacks %q:\n%s", want, out)
		}
	}
}

func TestShellCheckRenders(t *testing.T) {
	dir := checkGhost(t, 20)
	outDir := filepath.Join(t.TempDir(), "render")
	if _, err := runShellCheck(t, "-C", dir, "-render", outDir); err != nil {
		t.Fatal(err)
	}
	read := func(name string) *image.NRGBA {
		t.Helper()
		f, err := os.Open(filepath.Join(outDir, name))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		img, err := png.Decode(f)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out := image.NewNRGBA(img.Bounds())
		for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
			for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
				out.Set(x, y, img.At(x, y))
			}
		}
		return out
	}

	def, sleepy, awake := read("default.png"), read("sleepy.png"), read("awake.png")
	// The ghost boots in its default pose, so that is what default.png
	// shows, whichever pose was rendered last.
	if !bytes.Equal(def.Pix, sleepy.Pix) {
		t.Error("default.png is not the default pose")
	}
	if bytes.Equal(def.Pix, awake.Pix) {
		t.Error("default.png matches a pose other than the default")
	}

	// The base is the body with no face: where the eyes sit, it shows body.
	base := read("base.png")
	if got := base.NRGBAAt(9, 8); got != checkBody {
		t.Errorf("base.png at the eyes = %v, want the body %v", got, checkBody)
	}

	// masks.png outlines the silhouette's edge and leaves its middle alone.
	masks := read("masks.png")
	if got := masks.NRGBAAt(2, 10); got == base.NRGBAAt(2, 10) {
		t.Errorf("masks.png does not outline the silhouette at its edge: %v", got)
	}
	if got := masks.NRGBAAt(10, 14); got != base.NRGBAAt(10, 14) {
		t.Errorf("masks.png paints inside the silhouette: %v", got)
	}
}
