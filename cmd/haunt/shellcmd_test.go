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

	"github.com/aaronjauregui/haunt/internal/ghost"
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

	t.Run("removes every shell table", func(t *testing.T) {
		in := "[ghost]\nname = \"a\"\n\n[[shell.layer]]\nname = \"old\"\n\n[[shell.clip]]\nname = \"blink\"\n\n[persona]\nsystem = \"s\"\n"
		out, err := replaceShellSection(in, section)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, `"old"`) || strings.Contains(out, "blink") {
			t.Errorf("a shell table survived:\n%s", out)
		}
		if !strings.Contains(out, `system = "s"`) {
			t.Errorf("the persona was eaten:\n%s", out)
		}
	})
}
