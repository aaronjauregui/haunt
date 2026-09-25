// Package files reads the folders a person shares with their ghosts: it
// lists them, pages through text and PDFs, prepares pictures for the vision
// model, and searches by name and by words inside. It never writes, moves
// or deletes anything, and it knows nothing of ghosts; internal/ghost wraps
// it in tools and gates.
//
// # Named roots
//
// Each shared folder is a root with a name, from [files.roots] in the
// person's config (notes = "~/notes"). The model names files as
// <root>/<rel> with forward slashes (notes/2026/todo.md), and that is the
// only form a path takes on the way out as well. A host path never reaches
// the model: an error from the operating system carries the absolute path
// it failed on, so every error is rewritten in root/rel words ("there is no
// notes/todo.md") before it leaves this package, and a last pass replaces
// any root's host directory that is still in a result with its name.
//
// # Confinement
//
// A path from the model is refused before it touches the disk when it is
// absolute, starts with ~, holds a backslash or a NUL byte, names no root,
// or climbs out of its root with "..". Everything else is opened through
// an [os.Root] for its folder, which resolves symlinks itself and refuses
// any that lead out of the root, with no gap between checking a path and
// using it. A symlink that stays inside the root is followed. Nothing else
// inside a root is off limits: the person chose the folder, and hidden
// files are listed and read like any other. Search skips .git and
// node_modules for speed, not safety; it does not descend symlinked
// folders, and it matches a symlinked file by name only.
//
// Only regular files are read. A file is opened without blocking and
// checked before a byte is read, because a FIFO would otherwise hang the
// read forever, and a device or socket is not a document.
//
// A root whose folder is missing (an unmounted drive) is listed as
// unavailable and opened again the next time it is asked for.
//
// # Kinds
//
// What a file is comes from its first 8 KiB, with the extension only as a
// tiebreak. %PDF- is a PDF; a PNG, JPEG, GIF or WebP signature is a
// picture; text is valid UTF-8 without a NUL byte (a rune split at the
// edge of the window is allowed, and a known text extension forgives
// invalid UTF-8). Anything else is refused.
//
// # Limits
//
// Limits are constants, not configuration: they stop a 2 GB log or a
// decompression bomb from hurting the driver, and no person needs to tune
// them.
//
//   - A read returns at most 1500 bytes of text, and every result this
//     package renders, envelope and footer included, is under 2000 bytes,
//     so the driver's clip on a tool result never lands on the footer that
//     says where to read on from.
//   - A text file is paged by byte offset and never loaded whole. A page
//     starts on a rune and ends on the last newline in its final quarter,
//     else on a rune.
//   - A PDF may be up to 50 MB and 300 pages, and its text is extracted
//     within 10 s, isolated from panics in the parser. The text is kept for
//     10 minutes, keyed by root, path, size and modification time, and
//     paged by rune offset.
//   - A picture may be up to 20 MB and 40 megapixels; its long edge is
//     scaled down to 1568 px before the vision model sees it.
//   - Search looks at no more than 5,000 files, reads no more than 50 MB,
//     skips the contents of files over 5 MB, stops at 100 matches and
//     after 5 s, and clips a matched line to 160 runes. A search that stops
//     at a limit says which one, so the model narrows the path rather than
//     concluding there is nothing.
//   - A listing shows at most 200 entries and says how many more there
//     are.
//
// # Use
//
// [Open] takes the roots from config, name to absolute directory, and
// returns [Roots] with a warning for each directory it could not open.
// [Roots.List], [Roots.Read] and [Roots.Search] take paths in root/rel form
// and return text already rendered for the model, each result in an
// envelope that says the words are from the person's folder and are not
// them speaking. [Roots.Read] returns a [File]: a page of text or of a
// PDF's text in File.Text, or, for a picture, a PNG in File.PNG for the
// caller to describe with the vision model and wrap with [RenderPicture].
// A read's from is a byte offset in a text file and a rune offset in a
// PDF's text; the footer of each page gives the next one. [Found] says
// whether a search showed any line from inside a file (HasContent), as
// opposed to names alone, and how many matches it found.
//
// An error that errors.Is [ErrRefused] is a path or file the rules forbid:
// one outside the shared folders, a special file, a kind that is not read.
// Every error, refused or not, is already worded in root/rel form and safe
// to show the model.
package files

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// fileChunkBytes is the most text one read returns, the same as a web
	// page's chunk: a few paragraphs, which is what the model can use.
	fileChunkBytes = 1500
	// resultMax bounds every result this package renders, in bytes. It
	// sits under the driver's clip on a tool result (toolResultChars in
	// internal/ghost), so that clip never removes the paging footer.
	resultMax = 1999
	// pathShown is the most of a path an envelope repeats. A path is the
	// model's own words coming back, and a long one must not eat the page.
	pathShown = 240
	// sniffBytes is how much of a file decides what kind it is.
	sniffBytes = 8 << 10
	// listMax is the most entries one listing shows.
	listMax = 200
	// pdfCacheTTL keeps a PDF's extracted text long enough to page through
	// it in conversation without parsing it again.
	pdfCacheTTL = 10 * time.Minute

	searchMaxFiles   = 5000
	searchMaxBytes   = 50 << 20
	searchSkipAbove  = 5 << 20
	searchMaxMatches = 100
	searchDeadline   = 5 * time.Second
	matchLineRunes   = 160
)

// DefaultQuestion is what the vision model is asked about a picture when
// the model reading it did not say.
const DefaultQuestion = "Describe this picture in two or three sentences, including any writing in it."

// ErrRefused marks an error that means this package would not, as opposed
// to could not: a path outside the shared folders, a kind of file it does
// not read. errors.Is tells the two apart.
var ErrRefused = errors.New("refused")

type refusal struct{ why string }

func (r refusal) Error() string        { return r.why }
func (r refusal) Is(target error) bool { return target == ErrRefused }

func refusedf(format string, a ...any) error { return refusal{fmt.Sprintf(format, a...)} }

// Roots is the set of folders the person shared, each opened as an
// [os.Root]. It is safe for concurrent use.
type Roots struct {
	mu    sync.Mutex
	roots []*root // sorted by name
}

type root struct {
	name string
	dir  string
	r    *os.Root // nil while the folder is unavailable
}

// Open opens each named folder. A folder that cannot be opened is kept as
// unavailable rather than failing the rest; the returned errors say which
// and why, for the person's log (they carry host paths and are not for the
// model). An empty or nil map shares nothing.
func Open(dirs map[string]string) (*Roots, []error) {
	rs := &Roots{}
	var warn []error
	for name, dir := range dirs {
		rt := &root{name: name, dir: dir}
		r, err := os.OpenRoot(dir)
		if err != nil {
			warn = append(warn, fmt.Errorf("shared folder %s is unavailable: %w", name, err))
		} else {
			rt.r = r
		}
		rs.roots = append(rs.roots, rt)
	}
	sort.Slice(rs.roots, func(i, j int) bool { return rs.roots[i].name < rs.roots[j].name })
	sort.Slice(warn, func(i, j int) bool { return warn[i].Error() < warn[j].Error() })
	return rs, warn
}

// Close closes every open root.
func (rs *Roots) Close() error {
	if rs == nil {
		return nil
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	var errs []error
	for _, rt := range rs.roots {
		if rt.r != nil {
			errs = append(errs, rt.r.Close())
			rt.r = nil
		}
	}
	return errors.Join(errs...)
}

// Names is the root names, sorted.
func (rs *Roots) Names() []string {
	if rs == nil {
		return nil
	}
	names := make([]string, len(rs.roots))
	for i, rt := range rs.roots {
		names[i] = rt.name
	}
	return names
}

// Empty says nothing is shared.
func (rs *Roots) Empty() bool { return rs == nil || len(rs.roots) == 0 }

// open is the root's os.Root, opening it again if it was unavailable. nil
// means it still is.
func (rs *Roots) open(rt *root) *os.Root {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rt.r == nil {
		if r, err := os.OpenRoot(rt.dir); err == nil {
			rt.r = r
		}
	}
	return rt.r
}

func (rs *Roots) find(name string) *root {
	for _, rt := range rs.roots {
		if rt.name == name {
			return rt
		}
	}
	return nil
}

// scrub replaces any root's host directory left in s with the root's name.
// Errors are already written in root/rel words; this is the second line,
// for a message some library composed.
func (rs *Roots) scrub(s string) string {
	for _, rt := range rs.roots {
		// A root at / has nothing to hide and nothing safe to replace.
		dir := strings.TrimRight(rt.dir, "/")
		if dir == "" {
			continue
		}
		s = strings.ReplaceAll(s, dir, rt.name)
	}
	return s
}

func (rs *Roots) scrubErr(err error) error {
	if err == nil {
		return nil
	}
	msg := rs.scrub(err.Error())
	if errors.Is(err, ErrRefused) {
		return refusal{msg}
	}
	return errors.New(msg)
}

// Kind is what a file was read as.
type Kind int

const (
	KindText Kind = iota
	KindPDF
	KindImage
)

func (k Kind) String() string {
	switch k {
	case KindPDF:
		return "pdf"
	case KindImage:
		return "image"
	}
	return "text"
}

// File is one read. For text and PDFs, Text is the page, already in its
// envelope, and HasText says it holds words from the file rather than only
// a note that there was nothing past the end. For a picture, PNG is the
// image prepared for the vision model; the caller describes it and renders
// the description with [RenderPicture].
type File struct {
	// Path is the file in root/rel form, the only form it is shown in.
	Path     string
	Kind     Kind
	Size     int64
	Modified time.Time
	// Pages is a PDF's page count, 0 for anything else.
	Pages   int
	Text    string
	HasText bool
	PNG     []byte
}

// pdfCache holds extracted PDF text so paging through one costs a single
// parse. A changed file has a new size or time, so a stale entry is never
// served for it.
var pdfCache = struct {
	sync.Mutex
	entries map[pdfKey]pdfEntry
}{entries: map[pdfKey]pdfEntry{}}

type pdfKey struct {
	root, rel string
	size      int64
	mtime     int64
}

type pdfEntry struct {
	text  string
	runes int
	pages int
	at    time.Time
}

func cachedPDF(k pdfKey) (pdfEntry, bool) {
	pdfCache.Lock()
	defer pdfCache.Unlock()
	e, ok := pdfCache.entries[k]
	if !ok {
		return pdfEntry{}, false
	}
	if time.Since(e.at) > pdfCacheTTL {
		delete(pdfCache.entries, k)
		return pdfEntry{}, false
	}
	return e, true
}

func cachePDF(k pdfKey, e pdfEntry) {
	pdfCache.Lock()
	defer pdfCache.Unlock()
	now := time.Now()
	for key, old := range pdfCache.entries {
		if now.Sub(old.at) > pdfCacheTTL {
			delete(pdfCache.entries, key)
		}
	}
	e.at = now
	pdfCache.entries[k] = e
}
