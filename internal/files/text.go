package files

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"
)

// textExts are extensions that make a file text even when its first bytes
// are not valid UTF-8: an old note in Latin-1 is still a note, and reads
// with the odd character replaced.
var textExts = map[string]bool{
	".txt": true, ".md": true, ".markdown": true, ".org": true, ".rst": true,
	".csv": true, ".tsv": true, ".log": true, ".tex": true,
}

// sniff decides what a file is from its first bytes. full says the window
// was filled, so a rune split at its edge is the window's doing, not the
// file's.
func sniff(head []byte, full bool, name string) (Kind, bool) {
	if bytes.HasPrefix(head, []byte("%PDF-")) {
		return KindPDF, true
	}
	switch http.DetectContentType(head) {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return KindImage, true
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return 0, false
	}
	if full {
		head = head[:len(head)-partialTail(head)]
	}
	if utf8.Valid(head) || textExts[strings.ToLower(path.Ext(name))] {
		return KindText, true
	}
	return 0, false
}

// partialTail is how many bytes at the end of b are the start of a rune
// that b cuts off.
func partialTail(b []byte) int {
	for i := 1; i <= utf8.UTFMax && i <= len(b); i++ {
		if runeStart(b[len(b)-i]) {
			if utf8.FullRune(b[len(b)-i:]) {
				return 0
			}
			return i
		}
	}
	return 0
}

// Read reads one file, a page at a time. from is where the page starts: a
// byte offset in a text file, a rune offset in a PDF's text, and ignored
// for a picture. A picture comes back prepared but not described; the
// caller asks the vision model and renders the answer with
// [RenderPicture].
func (rs *Roots) Read(ctx context.Context, p string, from int64) (File, error) {
	f, err := rs.read(ctx, p, from)
	if err != nil {
		return File{}, rs.scrubErr(err)
	}
	f.Text = rs.scrub(f.Text)
	return f, nil
}

func (rs *Roots) read(ctx context.Context, p string, from int64) (File, error) {
	t, err := rs.parse(p)
	if err != nil {
		return File{}, err
	}
	r, err := rs.osRoot(t)
	if err != nil {
		return File{}, err
	}
	fh, info, err := openRegular(r, t)
	if err != nil {
		return File{}, err
	}
	defer fh.Close()
	f := File{Path: t.shown(), Size: info.Size(), Modified: info.ModTime()}

	head := make([]byte, sniffBytes)
	n, err := fh.ReadAt(head, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return File{}, osErr(t, err)
	}
	kind, ok := sniff(head[:n], n == sniffBytes && info.Size() > sniffBytes, t.rel)
	if !ok {
		return File{}, refusedf("%s is not a text file, a PDF or a picture", display(f.Path))
	}
	f.Kind = kind
	switch kind {
	case KindImage:
		png, err := prepareImage(fh, info.Size())
		if err != nil {
			return File{}, errors.New("could not read the picture " + display(f.Path) + ": " + err.Error())
		}
		f.PNG = png
	case KindPDF:
		key := pdfKey{root: t.rt.name, rel: t.rel, size: info.Size(), mtime: info.ModTime().UnixNano()}
		e, ok := cachedPDF(key)
		if !ok {
			text, pages, err := extractPDF(ctx, fh, info.Size())
			if err != nil {
				return File{}, errors.New("could not read the PDF " + display(f.Path) + ": " + err.Error())
			}
			e = pdfEntry{text: text, runes: utf8.RuneCountInString(text), pages: pages}
			cachePDF(key, e)
		}
		if strings.TrimSpace(e.text) == "" {
			return File{}, errors.New("that PDF has no text in it (a scan?)")
		}
		f.Pages = e.pages
		pdfPage(&f, e, from)
	default:
		if err := textPage(&f, fh, from); err != nil {
			return File{}, osErr(t, err)
		}
	}
	return f, nil
}

// openRegular opens a file for reading only if it is a regular file. The
// open does not block, so a FIFO cannot hang it, and the check is made on
// what was opened, so nothing can be swapped in between.
func openRegular(r *os.Root, t target) (*os.File, os.FileInfo, error) {
	fh, err := r.OpenFile(t.rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, osErr(t, err)
	}
	info, err := fh.Stat()
	if err != nil {
		fh.Close()
		return nil, nil, osErr(t, err)
	}
	if info.IsDir() {
		fh.Close()
		return nil, nil, refusedf("%s is a folder: list_files shows what is in it", display(t.shown()))
	}
	if !info.Mode().IsRegular() {
		fh.Close()
		return nil, nil, refusedf("%s is not a regular file", display(t.shown()))
	}
	return fh, info, nil
}

// textPage renders the page of a text file starting at the byte offset
// from, reading only that page off the disk.
func textPage(f *File, r io.ReaderAt, from int64) error {
	header := textHeader(*f)
	room := min(fileChunkBytes, resultMax-len(header)-footerRoom)
	size := f.Size
	if from < 0 {
		from = 0
	}
	if from >= size {
		f.Text = header + pastEnd(from, size)
		return nil
	}
	buf := make([]byte, room+utf8.UTFMax)
	n, err := r.ReadAt(buf, from)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	buf = buf[:n]
	// Start on a rune: a from inside one moves forward to the next.
	skip := 0
	for skip < len(buf) && skip < utf8.UTFMax-1 && !runeStart(buf[skip]) {
		skip++
	}
	start := from + int64(skip)
	chunk := buf[skip:]
	if len(chunk) > room {
		chunk = chunk[:room]
	}
	atEnd := start+int64(len(chunk)) >= size
	chunk = chunk[:cut(chunk, atEnd)]
	end := start + int64(len(chunk))

	text := strings.ToValidUTF8(string(chunk), "�")
	text = strings.ReplaceAll(text, "\x00", "")
	f.Text = header + strings.TrimRight(text, "\n") + "\n" + pageFooter(end, size)
	f.HasText = strings.TrimSpace(text) != ""
	return nil
}

// pdfPage renders the page of a PDF's text starting at the rune offset
// from.
func pdfPage(f *File, e pdfEntry, from int64) {
	header := textHeader(*f)
	room := min(fileChunkBytes, resultMax-len(header)-footerRoom)
	total := int64(e.runes)
	if from < 0 {
		from = 0
	}
	if from >= total {
		f.Text = header + pastEnd(from, total)
		return
	}
	// The rune offset to a byte offset; the text is a few MB at most.
	b := 0
	for i := int64(0); i < from; i++ {
		_, n := utf8.DecodeRuneInString(e.text[b:])
		b += n
	}
	chunk := []byte(e.text[b:min(len(e.text), b+room)])
	atEnd := b+len(chunk) >= len(e.text)
	chunk = chunk[:cut(chunk, atEnd)]
	end := from + int64(utf8.RuneCount(chunk))
	f.Text = header + strings.TrimSpace(string(chunk)) + "\n" + pageFooter(end, total)
	f.HasText = strings.TrimSpace(string(chunk)) != ""
}

// cut is where a page ends: all of it at the end of the file; else just
// after the last newline in its final quarter, so a read stops mid-line as
// rarely as possible without wasting the read; else on the last whole
// rune.
func cut(chunk []byte, atEnd bool) int {
	if atEnd {
		return len(chunk)
	}
	if i := bytes.LastIndexByte(chunk[len(chunk)*3/4:], '\n'); i >= 0 {
		return len(chunk)*3/4 + i + 1
	}
	if n := len(chunk) - partialTail(chunk); n > 0 {
		return n
	}
	return len(chunk)
}

func pastEnd(from, total int64) string {
	return "(nothing to read: from=" + strconv.FormatInt(from, 10) + " is past the end, which is " + strconv.FormatInt(total, 10) + ")\n[end of file]"
}
