package hermesskills

import (
	"io/fs"
	"slices"
	"testing"
)

func TestNames(t *testing.T) {
	want := []string{"humanizer", "meeting-action-items", "songwriting-and-ai-music", "weekly-review-planning"}
	if got := Names(); !slices.Equal(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
}

func TestOnly(t *testing.T) {
	fsys := Only([]string{"humanizer"})
	var files []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(files, []string{"humanizer/SKILL.md"}) {
		t.Fatalf("files = %v", files)
	}
	if _, err := fs.Stat(fsys, "weekly-review-planning/SKILL.md"); err == nil {
		t.Fatal("a skill not asked for is visible")
	}
	if _, err := fs.ReadFile(fsys, "humanizer/LICENSE"); err == nil {
		t.Fatal("only SKILL.md is embedded")
	}
}
