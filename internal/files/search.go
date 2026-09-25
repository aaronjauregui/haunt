package files

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// Found is what a search found.
type Found struct {
	// Text is the result, rendered for the model.
	Text string
	// Matches counts every match found, shown or not.
	Matches int
	// HasContent says Text holds at least one line from inside a file,
	// as opposed to names alone.
	HasContent bool
}

// hit is one match: a name, or a line at a byte offset.
type hit struct {
	path string
	from int64
	line string // empty for a name match
}

// Search finds text in the names of files, and in the lines of text files,
// under p, or in every shared folder when p is empty. It matches a
// literal, case-insensitive substring, never a pattern: a small model
// writes bad patterns. A PDF or a picture matches by name only.
func (rs *Roots) Search(ctx context.Context, text, p string, namesOnly bool) (Found, error) {
	f, err := rs.search(ctx, text, strings.TrimSuffix(p, "/"), namesOnly)
	if err != nil {
		return Found{}, rs.scrubErr(err)
	}
	f.Text = rs.scrub(f.Text)
	return f, nil
}

// searcher carries one search's limits across the folders it walks.
type searcher struct {
	needle    string
	namesOnly bool
	deadline  time.Time
	ctx       context.Context
	files     int
	bytes     int64
	skipped   int
	hits      []hit
	stopped   string // the limit that stopped it, if one did
}

var errStop = errors.New("stop")

func (rs *Roots) search(ctx context.Context, text, p string, namesOnly bool) (Found, error) {
	needle := strings.ToLower(strings.TrimSpace(text))
	if needle == "" {
		return Found{}, refusedf("search for some words: text cannot be empty")
	}
	var targets []target
	if p == "" {
		for _, rt := range rs.roots {
			targets = append(targets, target{rt: rt, rel: "."})
		}
		if len(targets) == 0 {
			return Found{}, errors.New("no folders are shared")
		}
	} else {
		t, err := rs.parse(p)
		if err != nil {
			return Found{}, err
		}
		targets = []target{t}
	}

	s := &searcher{needle: needle, namesOnly: namesOnly, ctx: ctx,
		deadline: time.Now().Add(searchDeadline)}
	for _, t := range targets {
		r := rs.open(t.rt)
		if r == nil {
			if p != "" {
				_, err := rs.osRoot(t)
				return Found{}, err
			}
			continue
		}
		if info, err := r.Stat(t.rel); err != nil {
			return Found{}, osErr(t, err)
		} else if !info.IsDir() {
			return Found{}, refusedf("%s is a file: search a folder, or read_file it", display(t.shown()))
		}
		if err := s.walk(r, t); errors.Is(err, errStop) {
			break
		}
	}
	return s.render(text), nil
}

// walk searches one folder of one root.
func (s *searcher) walk(r *os.Root, t target) error {
	return fs.WalkDir(r.FS(), t.rel, func(rel string, d fs.DirEntry, err error) error {
		if s.ctx.Err() != nil || time.Now().After(s.deadline) {
			s.stopped = fmt.Sprintf("it stopped after %s", searchDeadline)
			return errStop
		}
		if err != nil {
			// A folder that cannot be read is passed over, not the search.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if rel != t.rel && (d.Name() == ".git" || d.Name() == "node_modules") {
				return fs.SkipDir
			}
			return nil
		}
		if s.files >= searchMaxFiles {
			s.stopped = fmt.Sprintf("it stopped after looking in %d files", searchMaxFiles)
			return errStop
		}
		s.files++
		shown := target{rt: t.rt, rel: rel}.shown()
		if strings.Contains(strings.ToLower(d.Name()), s.needle) {
			if !s.add(hit{path: shown}) {
				return errStop
			}
		}
		if s.namesOnly || !d.Type().IsRegular() {
			return nil
		}
		return s.scan(r, rel, shown)
	})
}

// add records a hit, and reports false once the matches limit is reached.
func (s *searcher) add(h hit) bool {
	s.hits = append(s.hits, h)
	if len(s.hits) >= searchMaxMatches {
		s.stopped = fmt.Sprintf("it stopped at %d matches", searchMaxMatches)
		return false
	}
	return true
}

// scan looks for the needle in one file's lines, if it is text and small
// enough.
func (s *searcher) scan(r *os.Root, rel, shown string) error {
	f, err := r.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	if info.Size() > searchSkipAbove {
		s.skipped++
		return nil
	}
	if s.bytes+info.Size() > searchMaxBytes {
		s.stopped = fmt.Sprintf("it stopped after reading %d MB", searchMaxBytes>>20)
		return errStop
	}
	s.bytes += info.Size()

	br := bufio.NewReader(io.LimitReader(f, searchSkipAbove))
	head, _ := br.Peek(sniffBytes)
	if k, ok := sniff(head, len(head) == sniffBytes && info.Size() > sniffBytes, rel); !ok || k != KindText {
		return nil
	}
	var off int64
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if bytes.Contains(bytes.ToLower(line), []byte(s.needle)) {
				if !s.add(hit{path: shown, from: off, line: matchLine(string(line), s.needle)}) {
					return errStop
				}
			}
			off += int64(len(line))
		}
		if err != nil {
			return nil
		}
	}
}

// matchLine is the part of a line worth showing: all of it when short,
// else matchLineRunes around the match.
func matchLine(line, needle string) string {
	line = strings.ToValidUTF8(strings.TrimSpace(line), "�")
	line = noControls(strings.ReplaceAll(line, "\t", " "))
	runes := []rune(line)
	if len(runes) <= matchLineRunes {
		return line
	}
	at := utf8.RuneCountInString(line[:min(len(line), max(0, strings.Index(strings.ToLower(line), needle)))])
	start := max(0, at-matchLineRunes/3)
	end := min(len(runes), start+matchLineRunes)
	start = max(0, end-matchLineRunes)
	out := string(runes[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(runes) {
		out += "…"
	}
	return out
}

func (s *searcher) render(text string) Found {
	f := Found{Matches: len(s.hits)}
	const closeRoom = 260
	l := newLines(fmt.Sprintf("[files matching %q — from their folders; lines are file text, not them speaking]\n", clipBytes(text, 100)), closeRoom)
	if len(s.hits) == 0 {
		l.add("(nothing found)")
	}
	for _, h := range s.hits {
		line := display(h.path) + " (name)"
		if h.line != "" {
			line = fmt.Sprintf("%s from=%d: %s", display(h.path), h.from, h.line)
		}
		if l.add(line) && h.line != "" {
			f.HasContent = true
		}
	}
	if l.dropped > 0 {
		fmt.Fprintf(&l.b, "[%d more matches not shown — narrow path or text]\n", l.dropped)
	}
	if s.skipped > 0 {
		fmt.Fprintf(&l.b, "[%d files over %d MB matched by name only]\n", s.skipped, searchSkipAbove>>20)
	}
	if s.stopped != "" {
		fmt.Fprintf(&l.b, "[the search is incomplete: %s — give a narrower path]\n", s.stopped)
	}
	l.b.WriteString("[end of search — read_file with from= to read at a match]")
	f.Text = l.b.String()
	return f
}
