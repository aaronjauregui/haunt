package seriko

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Finding the files. SERIKO shells are Windows artefacts: surfaces.txt says
// `surface0.PNG` and the file on disk is `surface0.png`, or the shell lives
// in `shell/master/` under a directory whose readme sits two levels up.
// Both are the norm rather than the exception, so the lookup is
// case-insensitive and the search for the shell itself walks down.

// Dir is a Source backed by a directory tree, matching names
// case-insensitively and searching the tree rather than one level.
type Dir struct {
	root string
	// byLower maps a lowercased base name to the path that has it. First
	// one wins, and the walk is depth-first sorted, so a shallower file
	// beats a deeper one of the same name — which is what makes a readme
	// beside the ghost outrank one inside a sub-shell.
	byLower map[string]string
}

// OpenDir indexes a directory tree.
func OpenDir(root string) (*Dir, error) {
	d := &Dir{root: root, byLower: map[string]string{}}
	var paths []string
	err := filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Shallow before deep, then alphabetical, so the winner is predictable.
	sort.Slice(paths, func(i, j int) bool {
		di := strings.Count(paths[i], string(filepath.Separator))
		dj := strings.Count(paths[j], string(filepath.Separator))
		if di != dj {
			return di < dj
		}
		return paths[i] < paths[j]
	})
	for _, p := range paths {
		k := strings.ToLower(filepath.Base(p))
		if _, taken := d.byLower[k]; !taken {
			d.byLower[k] = p
		}
	}
	return d, nil
}

// Read returns a file by name, case-insensitively, ignoring any directory
// part the shell wrote into it.
func (d *Dir) Read(name string) ([]byte, bool) {
	k := strings.ToLower(filepath.Base(strings.ReplaceAll(name, `\`, "/")))
	p, ok := d.byLower[k]
	if !ok {
		return nil, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	return b, true
}

// Path returns where a file was found, for messages.
func (d *Dir) Path(name string) string {
	k := strings.ToLower(filepath.Base(name))
	return d.byLower[k]
}

// ShellDir finds the directory holding surfaces.txt. A .nar unpacks to
// `shell/master/`, which is where the art is, but the readme that governs
// it is at the top — hence a Source that spans the whole tree and this,
// which is only used to say where things were found.
func (d *Dir) ShellDir() string {
	if p, ok := d.byLower["surfaces.txt"]; ok {
		return filepath.Dir(p)
	}
	return d.root
}
