// Package hermesskills embeds the bundled hermes skills a ghost manifest
// may ask for by name (README.haunt.md).
package hermesskills

import (
	"embed"
	"io/fs"
	"path"
	"slices"
	"strings"
)

//go:embed */SKILL.md
var all embed.FS

// Names lists the skills available, sorted.
func Names() []string {
	entries, _ := fs.ReadDir(all, ".")
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// Only returns the named skills, rooted so each is a top-level directory.
// Unknown names are ignored; the manifest refuses them first.
func Only(names []string) fs.FS {
	return only{names: names}
}

type only struct{ names []string }

func (o only) allowed(name string) bool {
	first, _, _ := strings.Cut(name, "/")
	return name == "." || slices.Contains(o.names, first)
}

func (o only) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) || !o.allowed(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return all.Open(name)
}

func (o only) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) || !o.allowed(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	entries, err := all.ReadDir(name)
	if err != nil || name != "." {
		return entries, err
	}
	return slices.DeleteFunc(entries, func(e fs.DirEntry) bool {
		return !o.allowed(path.Clean(e.Name()))
	}), nil
}
