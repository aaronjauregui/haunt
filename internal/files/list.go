package files

import (
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// List says what is at p. No path lists the shared folders, marking any
// that are unavailable; a folder lists its entries, folders first and then
// by name; a file is its one line.
func (rs *Roots) List(p string) (string, error) {
	text, err := rs.list(strings.TrimSuffix(p, "/"))
	if err != nil {
		return "", rs.scrubErr(err)
	}
	return rs.scrub(text), nil
}

func (rs *Roots) list(p string) (string, error) {
	if p == "" {
		return rs.listRoots(), nil
	}
	t, err := rs.parse(p)
	if err != nil {
		return "", err
	}
	r, err := rs.osRoot(t)
	if err != nil {
		return "", err
	}
	info, err := r.Stat(t.rel)
	if err != nil {
		return "", osErr(t, err)
	}
	if !info.IsDir() {
		return entryLine(display(t.shown()), info), nil
	}
	dir, err := r.Open(t.rel)
	if err != nil {
		return "", osErr(t, err)
	}
	defer dir.Close()
	ents, err := dir.ReadDir(-1)
	if err != nil {
		return "", osErr(t, err)
	}

	type entry struct {
		name  string
		isDir bool
		line  string
	}
	var es []entry
	for _, d := range ents {
		sub := t.join(d.Name())
		name := display(d.Name())
		// Stat through the root, so a link is shown as what it leads to,
		// and one that leads out is shown as that and nothing more.
		info, err := r.Stat(sub.rel)
		switch {
		case isEscape(err):
			es = append(es, entry{name: d.Name(), line: name + " (a link out of the folder)"})
		case err != nil:
			es = append(es, entry{name: d.Name(), line: name + " (cannot be read)"})
		case info.IsDir():
			es = append(es, entry{name: d.Name(), isDir: true, line: name + "/ · changed " + day(info.ModTime())})
		default:
			es = append(es, entry{name: d.Name(), line: entryLine(name, info)})
		}
	}
	sort.Slice(es, func(i, j int) bool {
		if es[i].isDir != es[j].isDir {
			return es[i].isDir
		}
		return es[i].name < es[j].name
	})

	const moreRoom = 80
	noun := "entries"
	if len(es) == 1 {
		noun = "entry"
	}
	l := newLines(fmt.Sprintf("[folder %s · %d %s]\n", display(t.shown()), len(es), noun), moreRoom)
	if len(es) == 0 {
		l.add("(empty)")
	}
	for i, e := range es {
		if i >= listMax {
			l.dropped += len(es) - i
			break
		}
		l.add(e.line)
	}
	if l.dropped > 0 {
		fmt.Fprintf(&l.b, "[%d more not shown — list a folder further down, or search_files by name]", l.dropped)
	} else {
		l.b.WriteString("[end of folder]")
	}
	return l.b.String(), nil
}

// entryLine is a file's line in a listing.
func entryLine(name string, info fs.FileInfo) string {
	if !info.Mode().IsRegular() {
		return name + " (not a regular file)"
	}
	return fmt.Sprintf("%s · %s · changed %s", name, humanSize(info.Size()), day(info.ModTime()))
}

func (rs *Roots) listRoots() string {
	if rs.Empty() {
		return "(no folders are shared)"
	}
	var b strings.Builder
	b.WriteString("[shared folders]\n")
	for _, rt := range rs.roots {
		if rs.open(rt) == nil {
			b.WriteString(rt.name + "/ (unavailable)\n")
		} else {
			b.WriteString(rt.name + "/\n")
		}
	}
	b.WriteString("[list one with list_files path=" + rs.roots[0].name + "]")
	return b.String()
}
