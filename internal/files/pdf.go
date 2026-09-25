package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/ledongthuc/pdf"
)

const (
	// pdfMaxBytes is the largest PDF that is parsed at all. The parser
	// holds what it decodes in memory, and a parse that overruns its
	// deadline cannot be stopped, so this is also what bounds the cost of
	// one that is abandoned.
	pdfMaxBytes = 50 << 20
	// pdfMaxPages is the most pages a PDF may declare. A book is past
	// what a ghost reads a page at a time; a notice, a manual or a paper
	// is well within it.
	pdfMaxPages = 300
	// pdfDeadline bounds one extraction. PDF parsers are slow or loop on
	// malformed input, and a reply waiting on one is worth giving up on
	// long before the tool timeout would.
	pdfDeadline = 10 * time.Second
)

var (
	errPDFTooBig      = fmt.Errorf("that PDF is too big to read (over %d MB)", pdfMaxBytes>>20)
	errPDFTooLong     = fmt.Errorf("that PDF is too long to read (over %d pages)", pdfMaxPages)
	errPDFTimeout     = errors.New("that PDF took too long to read")
	errPDFUnreadable  = errors.New("that PDF could not be read")
	errPDFNoPageCount = errors.New("that PDF could not be read: it says nothing of its pages")
)

// extractPDF returns the text layer of the PDF in r, page after page, and
// how many pages it has. A PDF with no text layer (a scan) gives empty
// text and no error: saying so is the caller's.
//
// The size and page caps are checked before any text is extracted. The
// extraction runs in its own goroutine under recover(), because PDF
// parsers panic on malformed input, and under the earlier of ctx's
// deadline and pdfDeadline. A parse that overruns is abandoned, not
// killed: it stops at the next page boundary, and until then it keeps its
// memory, which pdfMaxBytes bounds. Errors name no path, since r has
// none.
func extractPDF(ctx context.Context, r io.ReaderAt, size int64) (text string, pages int, err error) {
	if size > pdfMaxBytes {
		return "", 0, errPDFTooBig
	}
	ctx, cancel := context.WithTimeout(ctx, pdfDeadline)
	defer cancel()

	type result struct {
		text  string
		pages int
		err   error
	}
	done := make(chan result, 1) // buffered: an abandoned parse must not block on sending
	go func() {
		var res result
		defer func() {
			if p := recover(); p != nil {
				res = result{err: fmt.Errorf("%w: %v", errPDFUnreadable, p)}
			}
			done <- res
		}()
		res.text, res.pages, res.err = parsePDF(ctx, r, size)
	}()

	select {
	case res := <-done:
		return res.text, res.pages, res.err
	case <-ctx.Done():
		return "", 0, errPDFTimeout
	}
}

// parsePDF is extractPDF without the guard around it. It may panic, and
// it checks ctx between pages so that an abandoned parse ends soon after.
func parsePDF(ctx context.Context, r io.ReaderAt, size int64) (string, int, error) {
	rd, err := pdf.NewReader(r, size)
	if err != nil {
		return "", 0, fmt.Errorf("%w: %v", errPDFUnreadable, err)
	}
	n := rd.NumPage()
	switch {
	case n > pdfMaxPages:
		return "", n, errPDFTooLong
	case n <= 0:
		return "", 0, errPDFNoPageCount
	}
	var (
		b      strings.Builder
		broken int
		first  any
	)
	for i := 1; i <= n; i++ {
		if err := ctx.Err(); err != nil {
			return "", 0, errPDFTimeout
		}
		t, p := readPage(rd, i)
		if p != nil {
			broken++
			if first == nil {
				first = p
			}
			t = fmt.Sprintf("[page %d could not be read]", i)
		}
		if t == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(t)
	}
	if broken == n {
		return "", n, fmt.Errorf("%w: %v", errPDFUnreadable, first)
	}
	return b.String(), n, nil
}

// readPage is the text of page i, or what it panicked with. A page that
// cannot be parsed is marked where it falls, and the pages around it are
// still read.
func readPage(rd *pdf.Reader, i int) (text string, panicked any) {
	defer func() {
		if p := recover(); p != nil {
			text, panicked = "", p
		}
	}()
	page := rd.Page(i)
	if page.V.IsNull() {
		return "", nil
	}
	return pageText(page.Content().Text), nil
}

// pageText lays a page's glyphs out as lines of text, in the order the
// page draws them. That order is the reading order for what word
// processors and browsers write, including a column at a time for
// two-column text, where sorting by position would interleave the
// columns line by line.
//
// A glyph lower on the page starts a new line, and a large drop or a
// jump back up (the top of the next column) leaves a blank line between.
// A gap along a line wider than the glyphs account for is a space. Many
// fonts give the parser no widths, so a run of glyphs drawn at one point
// is measured with glyphEms.
func pageText(glyphs []pdf.Text) string {
	var (
		b          strings.Builder
		started    bool
		lineY      float64 // baseline of the current line
		lineSize   float64 // font size the current line started with
		runX       float64 // where the current run of glyphs was placed
		runN       int     // glyphs in the current run
		runW       float64 // the current run's estimated width
		end        float64 // where the last glyph is estimated to end
		lastSpaced bool    // the last thing written was whitespace
	)
	for _, g := range glyphs {
		if g.S == "" || g.S == "\n" || g.S == "\r" {
			continue // the parser's own marker after each TJ
		}
		size := math.Abs(g.FontSize)
		if size == 0 {
			size = 1
		}
		switch {
		case !started:
			started = true
			lineY, lineSize = g.Y, size
		case math.Abs(g.Y-lineY) > 0.5*math.Min(size, lineSize):
			drop := lineY - g.Y
			if drop < 0 || drop > 1.9*math.Min(size, lineSize) {
				b.WriteString("\n\n")
			} else {
				b.WriteString("\n")
			}
			lineY, lineSize = g.Y, size
			lastSpaced = true
			runN = 0
		case g.X == runX && runN > 0:
			// the same run: a font without widths does not move the pen
		case !lastSpaced && (g.X-end > 0.3*size || g.X < runX-0.5*size):
			b.WriteByte(' ')
			lastSpaced = true
		}
		if g.X != runX || runN == 0 {
			runX, runN, runW = g.X, 0, 0
		}
		runN++
		if g.W > 0 {
			end = g.X + g.W
		} else {
			runW += glyphEms(g.S) * size
			end = runX + runW
		}
		s := g.S
		if lastSpaced {
			s = strings.TrimLeft(s, " \t")
		}
		if s == "" {
			continue
		}
		b.WriteString(s)
		lastSpaced = strings.HasSuffix(s, " ") || strings.HasSuffix(s, "\t")
	}
	return tidyLines(b.String())
}

// glyphEms guesses how wide a glyph is, in ems, for a font that gives
// no widths: near enough that a kerned "Wa" is not read as two words.
func glyphEms(s string) float64 {
	var w float64
	for _, r := range s {
		switch {
		case strings.ContainsRune("MWmw@", r):
			w += 0.9
		case strings.ContainsRune(" .,:;'!|ijlft()[]", r):
			w += 0.3
		case r >= 'A' && r <= 'Z':
			w += 0.7
		case r >= 0x2E80:
			w += 1 // CJK and the like are an em square
		default:
			w += 0.5
		}
	}
	return w
}

// tidyLines trims each line's trailing space, collapses runs of blank
// lines to one, and trims the whole.
func tidyLines(s string) string {
	lines := strings.Split(s, "\n")
	out := lines[:0]
	blank := false
	for _, l := range lines {
		l = strings.TrimRight(l, " \t ")
		if l == "" {
			if blank {
				continue
			}
			blank = true
		} else {
			blank = false
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
