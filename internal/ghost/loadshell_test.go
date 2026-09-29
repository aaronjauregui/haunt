package ghost

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/aaronjauregui/haunt/internal/shell"
)

// TestLoadShellPlaceholderIsScaled: a ghost with no art wears the full
// placeholder, poses and all, drawn at its effective scale rather than a
// fixed 160 pixels, and never smaller than 16.
func TestLoadShellPlaceholderIsScaled(t *testing.T) {
	for _, tc := range []struct {
		name   string
		own    float64
		factor float64
		size   int
	}{
		{"defaults", 0, 0, 160},
		{"a factor of one", 0, 1, 160},
		{"the person's factor", 0, 0.5, 80},
		{"the shell's own scale", 2, 0, 320},
		{"both, multiplied", 0.5, 1.5, 120},
		{"never below 16", 0.05, 1, 16},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := loadShell(t.TempDir(), shell.Manifest{Scale: tc.own}, tc.factor)
			if err != nil {
				t.Fatal(err)
			}
			if w, h := s.Size(); w != tc.size || h != tc.size {
				t.Errorf("placeholder is %dx%d, want %dx%d", w, h, tc.size, tc.size)
			}
			// The full placeholder, not a faceless body: it has the poses
			// the driver chooses from.
			if len(s.Poses()) == 0 {
				t.Error("the placeholder has no poses")
			}
		})
	}
}

// TestLoadShellPassesTheFactor: the person's factor reaches the loader for
// a shell with art of its own too.
func TestLoadShellPassesTheFactor(t *testing.T) {
	dir := t.TempDir()
	img := image.NewNRGBA(image.Rect(0, 0, 40, 20))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	f, err := os.Create(filepath.Join(dir, "ghost.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	f.Close()

	m := shell.Manifest{Sprite: "ghost.png"}
	s, err := loadShell(dir, m, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Scale(); got != 2 {
		t.Errorf("Scale() = %v, want 2", got)
	}
	if w, h := s.Size(); w != 80 || h != 40 {
		t.Errorf("a 40x20 sprite at factor 2 is %dx%d, want 80x40", w, h)
	}

	one, err := loadShell(dir, m, 1)
	if err != nil {
		t.Fatal(err)
	}
	if w, h := one.Size(); w != 40 || h != 20 {
		t.Errorf("a 40x20 sprite at factor 1 is %dx%d", w, h)
	}
}
