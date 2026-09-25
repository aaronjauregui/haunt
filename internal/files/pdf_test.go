package files

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// The two browser-made fixtures are testdata/pdf-text.pdf and
// testdata/pdf-two-column.pdf, printed by headless Chromium from the
// .html beside each:
//
//	chromium --headless --no-pdf-header-footer --print-to-pdf=pdf-text.pdf pdf-text.html
//
// They are real-world output: subset CID fonts with no widths the parser
// can use, a word to a text object. Every other fixture is written here by
// writePDF, so its structure is in plain sight.

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func extract(t *testing.T, b []byte) (string, int, error) {
	t.Helper()
	return extractPDF(context.Background(), bytes.NewReader(b), int64(len(b)))
}

func TestAPDFsTextComesOutInReadingOrder(t *testing.T) {
	text, pages, err := extract(t, readFixture(t, "pdf-text.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if pages != 1 {
		t.Errorf("pages = %d, want 1", pages)
	}
	want := "Dentist appointment\n\n" +
		"The dentist is on Tuesday at half past nine. Bring the insurance card and the letter from March.\n\n" +
		"Second paragraph: remember to ask about the crown."
	if text != want {
		t.Errorf("text =\n%s\nwant\n%s", text, want)
	}
}

func TestATwoColumnPDFIsReadAColumnAtATime(t *testing.T) {
	text, _, err := extract(t, readFixture(t, "pdf-two-column.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	order := []string{
		"Two columns",
		"Alpha begins the left column. The left column tells\nof the garden in spring",
		"Beta continues on the left. Water in the evening,",
		"Gamma opens the right column.",
		"Delta ends the right column.",
		"labelled jars.",
	}
	at := 0
	for _, s := range order {
		i := strings.Index(text[at:], s)
		if i < 0 {
			t.Fatalf("%q is missing or out of order in\n%s", s, text)
		}
		at += i + len(s)
	}
}

func TestAPDFWithSimpleFontsKeepsItsLines(t *testing.T) {
	b := writePDF([]string{
		pdfTextPage("Shopping list", "Eggs, flour and milk.", "Ask about the (cheap) oranges."),
		pdfTextPage("Page two"),
	}, false)
	text, pages, err := extract(t, b)
	if err != nil {
		t.Fatal(err)
	}
	want := "Shopping list\nEggs, flour and milk.\nAsk about the (cheap) oranges.\n\nPage two"
	if pages != 2 || text != want {
		t.Errorf("got %d pages, text =\n%q\nwant 2 pages,\n%q", pages, text, want)
	}
}

func TestAScannedPDFHasNoTextAndNoError(t *testing.T) {
	text, pages, err := extract(t, writePDF([]string{imagePage, imagePage}, true))
	if err != nil {
		t.Fatal(err)
	}
	if text != "" || pages != 2 {
		t.Errorf("got %d pages, text %q; want 2 pages and no text", pages, text)
	}
}

func TestABrokenPDFIsAnErrorNotAPanic(t *testing.T) {
	good := readFixture(t, "pdf-text.pdf")
	scribbled := bytes.Clone(good)
	for i := len(good) / 4; i < len(good)*3/4; i += 7 {
		scribbled[i] ^= 0x5a
	}
	cases := map[string][]byte{
		"truncated":                good[:len(good)/2],
		"header only":              []byte("%PDF-1.7\n"),
		"garbage":                  append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte{0xff, 0x00, '('}, 3000)...),
		"scribbled":                scribbled,
		"no page count":            []byte(strings.Replace(string(writePDF([]string{pdfTextPage("x")}, false)), "/Count 1", "/Count 0", 1)),
		"bad operator":             writePDF([]string{"BT /F1 12 Tf 72 720 Tm (x) Tj ET"}, false),
		"no cross-reference table": []byte("%PDF-1.4\n1 0 obj << /Type /Catalog /Pages 1 0 R >> endobj\nxref\n0 2\n0000000000 65535 f \n0000000009 00000 n \ntrailer << /Size 2 /Root 1 0 R >>\nstartxref\n62\n%%EOF\n"),
	}
	for name, b := range cases {
		t.Run(name, func(t *testing.T) {
			text, _, err := extract(t, b)
			if name == "scribbled" && err == nil {
				// scribbling may leave a parseable file; not panicking is the test
				return
			}
			if err == nil {
				t.Fatalf("no error, text %q", text)
			}
			if !strings.HasPrefix(err.Error(), "that PDF") {
				t.Errorf("error %q is not written for the model", err)
			}
		})
	}
}

func TestABrokenPageIsMarkedAndTheRestRead(t *testing.T) {
	b := writePDF([]string{pdfTextPage("First"), "BT /F1 12 Tf 72 720 Tm (x) Tj ET", pdfTextPage("Third")}, false)
	text, pages, err := extract(t, b)
	if err != nil {
		t.Fatal(err)
	}
	want := "First\n\n[page 2 could not be read]\n\nThird"
	if pages != 3 || text != want {
		t.Errorf("got %d pages, text %q; want 3, %q", pages, text, want)
	}
}

// panicReader stands in for a parser that panics partway through.
type panicReader struct{}

func (panicReader) ReadAt([]byte, int64) (int, error) { panic("index out of range") }

func TestAPanicInTheParserIsAnError(t *testing.T) {
	_, _, err := extractPDF(context.Background(), panicReader{}, 1000)
	if !errors.Is(err, errPDFUnreadable) {
		t.Fatalf("err = %v, want errPDFUnreadable", err)
	}
}

// stuckReader stands in for a parser that never finishes.
type stuckReader struct{ release chan struct{} }

func (s stuckReader) ReadAt([]byte, int64) (int, error) {
	<-s.release
	return 0, errors.New("released")
}

func TestAParseThatOverrunsIsAbandoned(t *testing.T) {
	r := stuckReader{release: make(chan struct{})}
	defer close(r.release)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := extractPDF(ctx, r, 1000)
	if !errors.Is(err, errPDFTimeout) {
		t.Fatalf("err = %v, want errPDFTimeout", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("gave up after %v", d)
	}
}

// failReader fails the test if anything reads it.
type failReader struct{ t *testing.T }

func (f failReader) ReadAt([]byte, int64) (int, error) {
	f.t.Error("a PDF over the size cap was read")
	return 0, errors.New("read")
}

func TestAPDFOverTheSizeCapIsNotRead(t *testing.T) {
	_, _, err := extractPDF(context.Background(), failReader{t}, pdfMaxBytes+1)
	if !errors.Is(err, errPDFTooBig) {
		t.Fatalf("err = %v, want errPDFTooBig", err)
	}
}

func TestAPDFOverThePageCapIsRefused(t *testing.T) {
	var pages []string
	for i := range pdfMaxPages + 1 {
		pages = append(pages, pdfTextPage(fmt.Sprintf("Page %d", i+1)))
	}
	_, n, err := extract(t, writePDF(pages, false))
	if !errors.Is(err, errPDFTooLong) {
		t.Fatalf("err = %v, want errPDFTooLong", err)
	}
	if n != pdfMaxPages+1 {
		t.Errorf("pages = %d, want %d", n, pdfMaxPages+1)
	}

	text, n, err := extract(t, writePDF(pages[:pdfMaxPages], false))
	if err != nil || n != pdfMaxPages || !strings.HasSuffix(text, "Page 300") {
		t.Errorf("at the cap: %d pages, err %v, text ends %q", n, err, text[max(0, len(text)-20):])
	}
}

// pdfTextPage is a content stream with one line of Helvetica per line, drawn
// the way simple generators do: one text object, a Td down each line.
func pdfTextPage(lines ...string) string {
	var b strings.Builder
	b.WriteString("BT /F1 12 Tf 72 720 Td\n")
	for i, l := range lines {
		if i > 0 {
			b.WriteString("0 -15 Td\n")
		}
		l = strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`).Replace(l)
		fmt.Fprintf(&b, "(%s) Tj\n", l)
	}
	b.WriteString("ET\n")
	return b.String()
}

// imagePage draws the image /Im0 over most of the page, as a scanner does.
const imagePage = "q 468 0 0 648 72 72 cm /Im0 Do Q\n"

// writePDF writes a PDF with one page per content stream, Helvetica as
// /F1 and, when image is set, an 8×8 grey image as /Im0.
func writePDF(contents []string, image bool) []byte {
	var objs []string // object n is objs[n-1]
	add := func(s string) int { objs = append(objs, s); return len(objs) }

	catalog := add("") // filled once the page tree's number is known
	pagesObj := add("")
	font := add("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	res := fmt.Sprintf("/Font << /F1 %d 0 R >>", font)
	if image {
		px := bytes.Repeat([]byte{0x80, 0x20}, 32)
		img := add(fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width 8 /Height 8 /ColorSpace /DeviceGray /BitsPerComponent 8 /Length %d >>\nstream\n%s\nendstream", len(px), px))
		res += fmt.Sprintf(" /XObject << /Im0 %d 0 R >>", img)
	}
	var kids []string
	for _, c := range contents {
		content := add(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(c), c))
		page := add(fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 612 792] /Resources << %s >> /Contents %d 0 R >>", pagesObj, res, content))
		kids = append(kids, fmt.Sprintf("%d 0 R", page))
	}
	objs[catalog-1] = fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pagesObj)
	objs[pagesObj-1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(kids))

	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root %d 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, catalog, xref)
	return b.Bytes()
}
