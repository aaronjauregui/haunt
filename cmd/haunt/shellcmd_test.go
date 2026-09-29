package main

import (
	"archive/zip"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/aaronjauregui/haunt/internal/ghost"
	"github.com/aaronjauregui/haunt/internal/shell"
)

// tinyORA writes a two-layer drawing: a body, and an eyes group with the
// open pair visible. Enough to exercise the command end to end.
func tinyORA(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)

	mt, _ := zw.Create("mimetype")
	mt.Write([]byte("image/openraster"))

	art := func(name string, r image.Rectangle, c color.NRGBA) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				img.SetNRGBA(x, y, c)
			}
		}
		if err := png.Encode(w, img); err != nil {
			t.Fatal(err)
		}
	}
	art("data/body.png", image.Rect(8, 10, 50, 60), color.NRGBA{40, 60, 200, 255})
	art("data/open.png", image.Rect(20, 20, 34, 26), color.NRGBA{255, 255, 255, 255})
	art("data/closed.png", image.Rect(20, 22, 34, 24), color.NRGBA{0, 0, 0, 255})

	sx, _ := zw.Create("stack.xml")
	fmt.Fprint(sx, `<?xml version="1.0" encoding="UTF-8"?>
<image w="64" h="64"><stack>
  <stack name="eyes">
    <layer name="open" src="data/open.png" x="0" y="0" visibility="visible"/>
    <layer name="closed" src="data/closed.png" x="0" y="0" visibility="hidden"/>
  </stack>
  <layer name="body" src="data/body.png" x="0" y="0" visibility="visible"/>
</stack></image>
`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

// `haunt shell init` writes the art and prints the section; with -write it
// puts the section in the manifest and leaves the rest of it alone.
func TestShellInitWritesArtAndManifest(t *testing.T) {
	dir := t.TempDir()
	ora := filepath.Join(dir, "wisp.ora")
	tinyORA(t, ora)

	manifest := `[ghost]
name = "wisp"
version = "0.1.0"
capabilities = ["message", "llm"]

[shell]
sprite = "old.png"

[persona]
system = "You are Wisp."
`
	if err := os.WriteFile(filepath.Join(dir, "ghost.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := cmdShell([]string{"init", "-C", dir, "-write", ora}); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"shell/body.png", "shell/eyes-open.png", "shell/eyes-closed.png"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(want))); err != nil {
			t.Errorf("missing %s: %v", want, err)
		}
	}

	// The manifest still loads, still knows who the ghost is, and now has
	// the layers. Re-running an import must not cost somebody their
	// persona.
	updated, err := os.ReadFile(filepath.Join(dir, "ghost.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "You are Wisp.") {
		t.Errorf("the persona was lost:\n%s", updated)
	}
	if strings.Contains(string(updated), "old.png") {
		t.Errorf("the previous shell section survived:\n%s", updated)
	}
	m, err := ghost.LoadManifest(dir)
	if err != nil {
		t.Fatalf("the updated manifest does not load: %v", err)
	}
	if len(m.Shell.Layers) != 2 {
		t.Errorf("layers = %+v, want body and eyes", m.Shell.Layers)
	}
	if m.Ghost.Name != "wisp" || m.Persona.System == "" {
		t.Errorf("manifest lost its identity: %+v", m.Ghost)
	}
}

// Without -write nothing is edited: the section goes to stdout, so a person
// can see what they are agreeing to before it lands in their manifest.
func TestShellInitLeavesTheManifestAloneByDefault(t *testing.T) {
	dir := t.TempDir()
	ora := filepath.Join(dir, "wisp.ora")
	tinyORA(t, ora)
	manifest := "[ghost]\nname = \"wisp\"\n"
	path := filepath.Join(dir, "ghost.toml")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := cmdShell([]string{"init", "-C", dir, ora}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != manifest {
		t.Errorf("the manifest was edited without -write:\n%s", after)
	}
}

func TestShellInitRejectsWhatItCannotRead(t *testing.T) {
	dir := t.TempDir()
	psd := filepath.Join(dir, "art.psd")
	if err := os.WriteFile(psd, []byte("8BPS"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := cmdShell([]string{"init", "-C", dir, psd})
	if err == nil {
		t.Fatal("a .psd was accepted")
	}
	// The error has to say what to do instead, because "unsupported" sends
	// somebody looking for a converter they already have in their drawing
	// program's export menu.
	if !strings.Contains(err.Error(), "Krita") {
		t.Errorf("error does not say where a .ora comes from: %v", err)
	}
}

// replaceShellSection is the risky half of -write: it edits a file someone
// wrote by hand.
func TestReplaceShellSection(t *testing.T) {
	const section = "[shell]\n\n[[shell.layer]]\nname = \"body\"\n"

	t.Run("replaces an existing one", func(t *testing.T) {
		in := "[ghost]\nname = \"a\"\n\n[shell]\nsprite = \"x.png\"\n\n[persona]\nsystem = \"s\"\n"
		out, err := replaceShellSection(in, section)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "x.png") {
			t.Errorf("old shell survived:\n%s", out)
		}
		for _, want := range []string{`name = "a"`, `system = "s"`, `name = "body"`} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("adds one when there is none", func(t *testing.T) {
		in := "[ghost]\nname = \"a\"\n\n[persona]\nsystem = \"s\"\n"
		out, err := replaceShellSection(in, section)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, `name = "body"`) || !strings.Contains(out, `system = "s"`) {
			t.Errorf("result:\n%s", out)
		}
	})

	t.Run("replaces layers and keeps clips", func(t *testing.T) {
		in := "[ghost]\nname = \"a\"\n\n[[shell.layer]]\nname = \"old\"\n\n[[shell.clip]]\nname = \"blink\"\n\n[persona]\nsystem = \"s\"\n"
		out, err := replaceShellSection(in, section)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, `"old"`) {
			t.Errorf("an old layer survived:\n%s", out)
		}
		clip := strings.Index(out, "[[shell.clip]]\nname = \"blink\"")
		if clip < 0 {
			t.Fatalf("the clip was lost:\n%s", out)
		}
		if layer := strings.Index(out, `name = "body"`); layer < 0 || layer > clip {
			t.Errorf("the clip does not follow the new layers:\n%s", out)
		}
		if persona := strings.Index(out, `system = "s"`); persona < clip {
			t.Errorf("the persona was eaten or moved:\n%s", out)
		}
	})
}

// writeMaskedORA writes a drawing with masks: a body, an eyes group, and a
// masks group with a silhouette and eyes. Big enough (600 px) that the
// scale suggestion applies.
func writeMaskedORA(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	mt, _ := zw.Create("mimetype")
	mt.Write([]byte("image/openraster"))
	art := func(name string, r image.Rectangle, c color.NRGBA) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		img := image.NewNRGBA(image.Rect(0, 0, 600, 600))
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				img.SetNRGBA(x, y, c)
			}
		}
		if err := png.Encode(w, img); err != nil {
			t.Fatal(err)
		}
	}
	white := color.NRGBA{255, 255, 255, 255}
	art("data/body.png", image.Rect(100, 100, 500, 580), color.NRGBA{40, 60, 200, 255})
	art("data/open.png", image.Rect(200, 200, 400, 240), white)
	art("data/closed.png", image.Rect(200, 218, 400, 222), color.NRGBA{0, 0, 0, 255})
	art("data/sil.png", image.Rect(90, 90, 510, 590), white)
	art("data/eyes-mask.png", image.Rect(190, 190, 410, 250), white)
	sx, _ := zw.Create("stack.xml")
	fmt.Fprint(sx, `<?xml version="1.0" encoding="UTF-8"?>
<image w="600" h="600"><stack>
  <stack name="masks" visibility="hidden">
    <layer name="eyes" src="data/eyes-mask.png" x="0" y="0" visibility="hidden"/>
    <layer name="silhouette" src="data/sil.png" x="0" y="0" visibility="hidden"/>
  </stack>
  <stack name="eyes">
    <layer name="open" src="data/open.png" x="0" y="0" visibility="visible"/>
    <layer name="closed" src="data/closed.png" x="0" y="0" visibility="hidden"/>
  </stack>
  <layer name="body" src="data/body.png" x="0" y="0" visibility="visible"/>
</stack></image>
`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

// -scale writes the scale; without it, the one already in the manifest
// stays. An out-of-range scale is refused before anything is written.
func TestShellInitScale(t *testing.T) {
	dir := t.TempDir()
	ora := filepath.Join(dir, "wisp.ora")
	writeMaskedORA(t, ora)
	path := filepath.Join(dir, "ghost.toml")
	manifest := "[ghost]\nname = \"wisp\"\n\n[shell]\nscale = 0.25 # a quarter\n"
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := cmdShell([]string{"init", "-C", dir, "-scale", "5", "-write", ora}); err == nil {
		t.Fatal("-scale 5 was accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "shell")); err == nil {
		t.Error("art was written despite a bad -scale")
	}

	if err := cmdShell([]string{"init", "-C", dir, "-write", ora}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "scale = 0.25 # a quarter") {
		t.Errorf("the existing scale was not kept:\n%s", got)
	}
	if !strings.Contains(string(got), `silhouette = "shell/masks/silhouette.png"`) {
		t.Errorf("no masks section:\n%s", got)
	}

	if err := cmdShell([]string{"init", "-C", dir, "-scale", "0.5", "-write", ora}); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "scale = 0.5\n") || strings.Contains(string(got), "0.25") {
		t.Errorf("-scale did not replace the scale:\n%s", got)
	}
	var parsed shellFile
	if _, err := toml.Decode(string(got), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Shell.Scale != 0.5 {
		t.Errorf("scale = %v, want 0.5", parsed.Shell.Scale)
	}
}

// rigged is a manifest an author has worked on: comments, a boot pose,
// rigging on the layers, poses, a clip and a hit, all written against
// tinyORA's drawing.
const rigged = `[ghost]
name = "wisp"
version = "0.1.0"
capabilities = ["message", "llm"]

# How it looks.
[shell]
default_pose = "idle"   # the boot face
sprite = "old.png"

[[shell.layer]]
name = "body"
image = "old-body.png"
region = "torso"

# The eyes ride on the body.
[[shell.layer]]
name = "eyes"
image = "old-eyes.png"
parent = "body"
region = "face"
pivot = [3, 4]

# Faces.
[shell.pose.idle]
eyes = "open"

[shell.pose.blink]
eyes = "closed"   # squeezed shut

[[shell.clip]]
name = "blink"
trigger = "sometimes"
[[shell.clip.keys]]
at = 0
set = { eyes = "closed" }
[[shell.clip.keys]]
at = 120
set = { eyes = "open" }

# Poke the nose.
[[shell.hit]]
region = "nose"
at = [1, 1]
size = [2, 2]

[persona]
system = "You are Wisp."
`

// A re-import replaces what the drawing describes and keeps what the
// author wrote: the other [shell] lines, poses, clips and hits verbatim,
// and the layers' parent, pivot and region.
func TestShellInitKeepsWhatTheAuthorWrote(t *testing.T) {
	dir := t.TempDir()
	ora := filepath.Join(dir, "wisp.ora")
	tinyORA(t, ora)
	path := filepath.Join(dir, "ghost.toml")
	if err := os.WriteFile(path, []byte(rigged), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := cmdShell([]string{"init", "-C", dir, "-write", ora}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)

	for _, gone := range []string{"old.png", "old-body.png", "old-eyes.png", "# The eyes ride on the body."} {
		if strings.Contains(got, gone) {
			t.Errorf("%q survived:\n%s", gone, got)
		}
	}
	keptBlocks := []string{
		"# How it looks.\n[shell]\n",
		`default_pose = "idle"   # the boot face`,
		"# Faces.\n[shell.pose.idle]\neyes = \"open\"",
		"[shell.pose.blink]\neyes = \"closed\"   # squeezed shut",
		"[[shell.clip]]\nname = \"blink\"\ntrigger = \"sometimes\"\n[[shell.clip.keys]]\nat = 0\nset = { eyes = \"closed\" }\n[[shell.clip.keys]]\nat = 120\nset = { eyes = \"open\" }",
		"# Poke the nose.\n[[shell.hit]]\nregion = \"nose\"\nat = [1, 1]\nsize = [2, 2]",
		"[persona]\nsystem = \"You are Wisp.\"",
	}
	last := -1
	for _, want := range keptBlocks {
		i := strings.Index(got, want)
		if i < 0 {
			t.Errorf("not kept verbatim: %q\n%s", want, got)
			continue
		}
		if i < last {
			t.Errorf("out of order: %q\n%s", want, got)
		}
		last = i
	}
	// The regenerated layers sit between the [shell] lines and the kept
	// tables.
	if layer, pose := strings.LastIndex(got, "[[shell.layer]]"), strings.Index(got, "[shell.pose"); layer < 0 || layer > pose {
		t.Errorf("the layers do not come before the kept tables:\n%s", got)
	}

	m, err := ghost.LoadManifest(dir)
	if err != nil {
		t.Fatalf("the updated manifest does not load: %v", err)
	}
	layers := map[string]shell.Layer{}
	for _, l := range m.Shell.Layers {
		layers[l.Name] = l
	}
	if l := layers["eyes"]; l.Parent != "body" || l.Region != "face" || l.Pivot != [2]int{3, 4} || l.Default != "open" {
		t.Errorf("eyes = %+v, want the drawing's variants with the old rigging", l)
	}
	if l := layers["body"]; l.Region != "torso" || l.Image != "shell/body.png" {
		t.Errorf("body = %+v", l)
	}
	if m.Shell.Default != "idle" || len(m.Shell.Clips) != 1 || len(m.Shell.Hits) != 1 || len(m.Shell.Poses) != 2 {
		t.Errorf("shell = %+v, want the pose, clip and hit kept", m.Shell)
	}
}

// A kept pose that names a variant the new drawing lacks fails the command,
// and the file stays written so the author can fix that one line.
func TestShellInitReportsAStalePose(t *testing.T) {
	dir := t.TempDir()
	ora := filepath.Join(dir, "wisp.ora")
	tinyORA(t, ora)
	path := filepath.Join(dir, "ghost.toml")
	stale := strings.Replace(rigged, `eyes = "closed"   # squeezed shut`, `eyes = "squint"`, 1)
	if err := os.WriteFile(path, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}

	err := cmdShell([]string{"init", "-C", dir, "-write", ora})
	if err == nil || !strings.Contains(err.Error(), "squint") {
		t.Fatalf("err = %v, want the stale pose reported", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `image = "shell/body.png"`) || !strings.Contains(string(b), `eyes = "squint"`) {
		t.Errorf("the manifest was not written:\n%s", b)
	}
}

// stderrOf runs fn and returns what it wrote to stderr.
func stderrOf(t *testing.T, fn func()) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = f
	defer func() { os.Stderr = saved }()
	fn()
	b, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Art at a painting's resolution with no scale anywhere gets a suggestion,
// and the closing message tells art from masks.
func TestShellInitSuggestsAScale(t *testing.T) {
	dir := t.TempDir()
	ora := filepath.Join(dir, "wisp.ora")
	writeMaskedORA(t, ora)
	var err error
	out := stderrOf(t, func() { err = cmdShell([]string{"init", "-C", dir, ora}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"3 layer images and 2 masks", "600x600", "-scale 0.43"} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr missing %q:\n%s", want, out)
		}
	}
	out = stderrOf(t, func() { err = cmdShell([]string{"init", "-C", dir, "-scale", "0.4", ora}) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "-scale 0.") {
		t.Errorf("a scale was suggested with -scale given:\n%s", out)
	}
}
