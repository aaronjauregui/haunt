package shell

import (
	"image/color"
	"slices"
	"testing"
)

// The note paper is art: named in the manifest, read at load, and drawn at
// the shell's scale and the person's factor like every layer. A file that
// is not there is a load error, not a plain sheet the first time a note is
// left.
func TestNoteArt(t *testing.T) {
	dir := t.TempDir()
	writeArt(t, dir, "body.png", solid(40, 40, color.NRGBA{R: 255, A: 255}))
	writeArt(t, dir, "note.png", solid(200, 120, color.NRGBA{R: 250, G: 240, B: 200, A: 255}))

	m := Manifest{Sprite: "body.png", Note: "note.png", Scale: 0.5}
	if !slices.Contains(m.Files(), "note.png") {
		t.Errorf("Files() = %v, want the note paper among them", m.Files())
	}
	s, err := LoadWith(dir, m, LoadOptions{ScaleFactor: 2})
	if err != nil {
		t.Fatal(err)
	}
	if s.Note() == nil {
		t.Fatal("the note paper did not load")
	}
	if w, h := s.Note().Size(); w != 200 || h != 120 {
		t.Errorf("at 0.5 times 2 the paper is %dx%d, want 200x120", w, h)
	}
	s = mustLoad(t, dir, m)
	if w, h := s.Note().Size(); w != 100 || h != 60 {
		t.Errorf("at 0.5 the paper is %dx%d, want 100x60", w, h)
	}
	// On a layered shell and on the placeholder too.
	layered := Manifest{Layers: []Layer{{Name: "body", Image: "body.png"}}, Note: "note.png"}
	if mustLoad(t, dir, layered).Note() == nil {
		t.Error("a layered shell lost its note paper")
	}
	if mustLoad(t, dir, Manifest{Note: "note.png"}).Note() == nil {
		t.Error("the placeholder lost its note paper")
	}
	// Without one there is none, and the driver draws its own.
	if mustLoad(t, dir, Manifest{Sprite: "body.png"}).Note() != nil {
		t.Error("a shell with no note paper has some")
	}

	_, err = Load(dir, Manifest{Sprite: "body.png", Note: "missing.png"})
	wantError(t, err, "shell note")
	_, err = Load(dir, Manifest{Sprite: "body.png", Note: "../note.png"})
	wantError(t, err, "inside the ghost directory")
}
