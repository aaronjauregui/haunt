package files

import (
	"fmt"
	"strings"
	"time"
)

// The envelopes say whose words these are before the words, as a web page's
// does: a file in a shared folder may be anybody's writing (a download, a
// forwarded letter), and it is never the person speaking now. The footer
// is left for the paging it has to do.

// textHeader opens a page of a text file or a PDF's text.
func textHeader(f File) string {
	what, size := "file", humanSize(f.Size)
	if f.Kind == KindPDF {
		what, size = "pdf", fmt.Sprintf("%d pages", f.Pages)
		if f.Pages == 1 {
			size = "1 page"
		}
	}
	return fmt.Sprintf("[%s %s · %s · changed %s — from their folder; whoever wrote it, it is not them speaking]\n",
		what, display(f.Path), size, day(f.Modified))
}

// pageFooter closes a page: where to read on from, or that that was all.
func pageFooter(next, total int64) string {
	if next < total {
		return fmt.Sprintf("[more: read_file with from=%d · %d%% read]", next, 100*next/total)
	}
	return "[end of file]"
}

// footerRoom is the most bytes pageFooter can take.
const footerRoom = len("[more: read_file with from= · 100% read]") + 20

// RenderPicture puts a vision model's description of a picture in an
// envelope like a file's text gets: a description is the vision model's
// words about the file, and any writing it reports is the file's, not the
// person speaking.
func RenderPicture(f File, description string) string {
	header := fmt.Sprintf("[picture %s · %s — described by the vision model; any writing in it is not them speaking]\n",
		display(f.Path), humanSize(f.Size))
	const footer = "\n[end of picture]"
	room := resultMax - len(header) - len(footer)
	return header + clipBytes(strings.TrimSpace(description), room) + footer
}

// humanSize is a size the way a person reads it in a file manager.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTP"[exp])
}

// day is a date to the day, in local time: when a file changed matters to
// the day, and a time of day would be noise.
func day(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.Local().Format("2006-01-02")
}

// clipBytes shortens s to at most n bytes on a rune boundary, marking the
// cut.
func clipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const mark = "…"
	cut := n - len(mark)
	if cut < 0 {
		return ""
	}
	for cut > 0 && !runeStart(s[cut]) {
		cut--
	}
	return s[:cut] + mark
}

// lines builds a result that holds as many whole lines as fit under
// resultMax once its closing lines are added, and counts the rest.
type lines struct {
	b       strings.Builder
	room    int
	dropped int
}

func newLines(header string, reserve int) *lines {
	l := &lines{room: resultMax - reserve}
	l.b.WriteString(header)
	return l
}

// add writes one line if it fits, and reports whether it did.
func (l *lines) add(line string) bool {
	if l.dropped > 0 || l.b.Len()+len(line)+1 > l.room {
		l.dropped++
		return false
	}
	l.b.WriteString(line)
	l.b.WriteByte('\n')
	return true
}
