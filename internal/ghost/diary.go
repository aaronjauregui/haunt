package ghost

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aaronjauregui/haunt/internal/memory"
)

// The diary is memory/diary.md: one dated entry per day the ghost and the
// person talked, newest last, written by the ghost's reflection on the day
// (reflect.go) through the diary decision. It is how a day is carried
// forward once its turns leave the prompt: compaction folds a whole day
// behind its entry (transcript.go) and never summarises it again.
//
//	# Diary
//
//	## 2026-10-11 Saturday
//	They moved to Brisbane last month. We talked about the sway config
//	most of the afternoon.
//
//	<!-- self t=212 2026-10-12 -->
//
// Each entry ends with the same source label as the notes (labelled in
// trust.go). The person may edit the file, and the driver keeps what they
// wrote: it only ever adds a new day's section, and never writes over a
// day that has one. It is read again for every prompt. Part 2 of the prompt shows the entries of the last diaryDays
// calendar days, each cut to diaryEntryLimit, and names a day with no
// entry, so a gap in the days reads as a gap.

const (
	// diaryDays is how many calendar days before today part 2 shows.
	diaryDays = 3
	// diaryEntryLimit bounds one entry, in characters, as written and as
	// shown.
	diaryEntryLimit = 600
	// dayForm is how a day is written: in the diary, the reflect
	// envelope and the transcript's diary rows.
	dayForm = "2006-01-02"
)

// diaryEntry is one day's section.
type diaryEntry struct {
	Day  string // dayForm
	Text string // the entry as written, label included
}

// body is the entry without its source label, whitespace folded.
func (e diaryEntry) body() string {
	return strings.Join(strings.Fields(labelLine.ReplaceAllString(e.Text, "")), " ")
}

// dayOf is the local calendar day t falls on.
func dayOf(t time.Time) string { return t.Local().Format(dayForm) }

// weekday names a day ("Saturday"), or "" when it does not parse.
func weekday(day string) string {
	t, err := time.ParseInLocation(dayForm, day, time.Local)
	if err != nil {
		return ""
	}
	return t.Weekday().String()
}

// parseDiary reads the file's day sections. A day's heading is "## " and
// a date; whatever follows the date on it is the person's. A section runs
// to the next "## " heading of any kind, so a section of the person's own
// ("## Notes") is not part of the day before it.
func parseDiary(data string) []diaryEntry {
	var out []diaryEntry
	var cur *diaryEntry
	for _, line := range strings.Split(data, "\n") {
		if day, ok := diaryHeading(line); ok {
			out = append(out, diaryEntry{Day: day})
			cur = &out[len(out)-1]
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "## ") {
			cur = nil
			continue
		}
		if cur != nil {
			cur.Text += line + "\n"
		}
	}
	for i := range out {
		out[i].Text = strings.TrimSpace(out[i].Text)
	}
	return out
}

// diaryHeading reads "## 2026-10-11 Saturday" as its day.
func diaryHeading(line string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "## ")
	if !ok || len(rest) < len(dayForm) {
		return "", false
	}
	day := rest[:len(dayForm)]
	if _, err := time.Parse(dayForm, day); err != nil {
		return "", false
	}
	return day, true
}

// readDiary is the diary under dir, oldest first; none when there is no
// file yet.
func readDiary(dir string) ([]diaryEntry, error) {
	data, err := readDiaryFile(dir)
	if err != nil {
		return nil, err
	}
	entries := parseDiary(data)
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Day < entries[j].Day })
	return entries, nil
}

func readDiaryFile(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, memory.DiaryFile))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return string(data), err
}

// errHasEntry is writeDiaryEntry refusing a day that has an entry.
var errHasEntry = errors.New("that day already has an entry")

// hasEntry is the one test of whether a day has an entry: a section with
// text in it. A heading with nothing under it is a day waiting for one.
func hasEntry(entries []diaryEntry, day string) (string, bool) {
	for _, e := range entries {
		if e.Day == day && e.body() != "" {
			return e.body(), true
		}
	}
	return "", false
}

// writeDiaryEntry adds one day's entry. Everything already in the file is
// kept byte for byte: the text goes under the day's heading if the file
// has one with nothing under it, and otherwise a new section goes in
// before the first day that is later than it, or at the end. A day that
// has an entry, written by the ghost or by hand, is refused (errHasEntry).
func writeDiaryEntry(dir, day, text string) error {
	data, err := readDiaryFile(dir)
	if err != nil {
		return err
	}
	if _, ok := hasEntry(parseDiary(data), day); ok {
		return errHasEntry
	}
	text = strings.TrimSpace(text) + "\n"
	section := "## " + day + " " + weekday(day) + "\n" + text
	if data == "" {
		return memory.WriteDiary(dir, "# Diary\n\n"+section)
	}
	lines := strings.SplitAfter(data, "\n")
	at, under := len(lines), false
	for i, line := range lines {
		if d, ok := diaryHeading(line); ok && d >= day {
			at, under = i, d == day
			break
		}
	}
	if under {
		head := lines[at]
		if !strings.HasSuffix(head, "\n") {
			head += "\n"
		}
		return memory.WriteDiary(dir, strings.Join(lines[:at], "")+head+text+strings.Join(lines[at+1:], ""))
	}
	before := strings.Join(lines[:at], "")
	if at < len(lines) {
		section += "\n"
	} else {
		if !strings.HasSuffix(before, "\n") {
			before += "\n"
		}
		if !strings.HasSuffix(before, "\n\n") {
			before += "\n"
		}
	}
	return memory.WriteDiary(dir, before+section+strings.Join(lines[at:], ""))
}

// diaryEntryFor is one day's entry text, label left out, if it has one.
func diaryEntryFor(dir, day string) (string, bool) {
	entries, err := readDiary(dir)
	if err != nil {
		return "", false
	}
	return hasEntry(entries, day)
}

// renderDiary is the diary as part 2 shows it: the diaryDays days before
// today, and today if it has an entry, each entry cut to diaryEntryLimit
// and a day without one named as such. Nothing while the diary has no
// entries at all, so a ghost that has never reflected shows no gap.
func renderDiary(entries []diaryEntry, now time.Time) string {
	if len(entries) == 0 {
		return ""
	}
	byDay := map[string]string{}
	for _, e := range entries {
		if b := e.body(); b != "" {
			byDay[e.Day] = b
		}
	}
	y, m, d := now.Local().Date()
	var lines []string
	for i := diaryDays; i >= 0; i-- {
		day := time.Date(y, m, d-i, 12, 0, 0, 0, time.Local).Format(dayForm)
		text, ok := byDay[day]
		switch {
		case ok:
			lines = append(lines, day+" "+weekday(day)+": "+cutEntry(text))
		case i > 0:
			lines = append(lines, day+": no entry")
		}
	}
	return "Your diary, last days:\n" + strings.Join(lines, "\n")
}

// cutEntry shortens an entry to diaryEntryLimit characters, marking the
// cut.
func cutEntry(s string) string {
	if utf8.RuneCountInString(s) <= diaryEntryLimit {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:diaryEntryLimit-1])) + "…"
}

// diary is the ghost's diary rendered for part 2, or "" for none.
func (b *modelBrain) diary(now time.Time) string {
	if b.memDir == "" {
		return ""
	}
	entries, err := readDiary(b.memDir)
	if err != nil {
		return ""
	}
	return renderDiary(entries, now)
}

// diaryEntry is a day's entry from the ghost's diary.
func (b *modelBrain) diaryEntry(day string) (string, bool) {
	if b.memDir == "" {
		return "", false
	}
	return diaryEntryFor(b.memDir, day)
}
