package ghost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// daysAgo is the local day n days before now, at the given hour.
func daysAgo(n, hour int) time.Time {
	y, m, d := time.Now().Date()
	return time.Date(y, m, d-n, hour, 0, 0, 0, time.Local)
}

// The diary in part 2 is the last diaryDays days, a day with no entry
// named as a gap, each entry cut to diaryEntryLimit, and nothing older.
func TestDiaryRender(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("We talked about the sway config again. ", 30)
	for _, e := range []struct {
		ago  int
		text string
	}{
		{5, "Too long ago to show."},
		{3, "They moved to Brisbane last month."},
		{1, long},
	} {
		if err := writeDiaryEntry(dir, dayOf(daysAgo(e.ago, 12)), e.text+"\n\n<!-- self t=4 2026-10-12 -->\n"); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "diary.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "# Diary\n\n## "+dayOf(daysAgo(5, 12))+" "+daysAgo(5, 12).Weekday().String()+"\n") {
		t.Errorf("the file does not open with the oldest day:\n%s", data)
	}
	entries, err := readDiary(dir)
	if err != nil || len(entries) != 3 {
		t.Fatalf("read back %d entries (%v)", len(entries), err)
	}
	got := renderDiary(entries, time.Now())
	lines := strings.Split(got, "\n")
	if lines[0] != "Your diary, last days:" || len(lines) != 4 {
		t.Fatalf("rendered:\n%s", got)
	}
	three, two, one := dayOf(daysAgo(3, 12)), dayOf(daysAgo(2, 12)), dayOf(daysAgo(1, 12))
	if lines[1] != three+" "+daysAgo(3, 12).Weekday().String()+": They moved to Brisbane last month." {
		t.Errorf("first day: %q", lines[1])
	}
	if lines[2] != two+": no entry" {
		t.Errorf("the gap: %q", lines[2])
	}
	if !strings.HasPrefix(lines[3], one) || !strings.HasSuffix(lines[3], "…") ||
		utf8.RuneCountInString(strings.SplitN(lines[3], ": ", 2)[1]) > diaryEntryLimit {
		t.Errorf("the long entry is not cut to %d characters: %d", diaryEntryLimit, utf8.RuneCountInString(lines[3]))
	}
	if got := cutEntry(strings.Repeat("é", 700)); utf8.RuneCountInString(got) != diaryEntryLimit {
		t.Errorf("cut counts bytes, not characters: %d", utf8.RuneCountInString(got))
	}
	if strings.Contains(got, "Too long ago") || strings.Contains(got, "<!--") {
		t.Errorf("an old entry or a label is shown:\n%s", got)
	}
	if renderDiary(nil, time.Now()) != "" {
		t.Error("an empty diary renders something")
	}

	// A day that has an entry keeps it.
	if err := writeDiaryEntry(dir, three, "Rewritten."); !errors.Is(err, errHasEntry) {
		t.Errorf("writing over an entry: %v", err)
	}
	if e, ok := diaryEntryFor(dir, three); !ok || e != "They moved to Brisbane last month." {
		t.Errorf("the entry after a refused rewrite: %q %v", e, ok)
	}
}

// What the person wrote in the diary stays byte for byte: the text before
// the first day, a heading with more after its date, a section of their
// own. A new day only adds its section, in date order.
func TestDiaryKeepsWhatThePersonWrote(t *testing.T) {
	dir := t.TempDir()
	mine := "# Diary\n\nWisp keeps this; I read it on Sundays.\n\n" +
		"## 2026-10-01 Thursday - the big move\nBoxes everywhere.\n\n" +
		"## Notes\nnothing to see here\n\n" +
		"## 2026-10-07 - fill this in\n\n" +
		"## 2026-10-09 Friday\nQuiet.\n"
	if err := os.WriteFile(filepath.Join(dir, "diary.md"), []byte(mine), 0o600); err != nil {
		t.Fatal(err)
	}
	if e, ok := diaryEntryFor(dir, "2026-10-01"); !ok || e != "Boxes everywhere." {
		t.Errorf("the hand-written day reads as %q %v", e, ok)
	}
	if err := writeDiaryEntry(dir, "2026-10-05", "Unpacked the kitchen."); err != nil {
		t.Fatal(err)
	}
	if err := writeDiaryEntry(dir, "2026-10-11", "Talked about the oven."); err != nil {
		t.Fatal(err)
	}
	// A heading with nothing under it is not an entry: the text goes under it.
	if _, ok := diaryEntryFor(dir, "2026-10-07"); ok {
		t.Error("an empty heading reads as an entry")
	}
	if err := writeDiaryEntry(dir, "2026-10-07", "Found the kettle."); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "diary.md"))
	want := "# Diary\n\nWisp keeps this; I read it on Sundays.\n\n" +
		"## 2026-10-01 Thursday - the big move\nBoxes everywhere.\n\n" +
		"## Notes\nnothing to see here\n\n" +
		"## 2026-10-05 " + weekday("2026-10-05") + "\nUnpacked the kitchen.\n\n" +
		"## 2026-10-07 - fill this in\nFound the kettle.\n\n" +
		"## 2026-10-09 Friday\nQuiet.\n\n" +
		"## 2026-10-11 " + weekday("2026-10-11") + "\nTalked about the oven.\n"
	if string(got) != want {
		t.Errorf("the file:\n%s\nwant:\n%s", got, want)
	}
	if err := writeDiaryEntry(dir, "2026-10-01", "Mine now."); !errors.Is(err, errHasEntry) {
		t.Errorf("writing over a hand-written day: %v", err)
	}
}

// seedDay writes n finished turns on a day, each the person's words and a
// long reply.
func seedDay(t *testing.T, tr *transcript, at time.Time, n int, words string) {
	t.Helper()
	for i := range n {
		id, err := tr.nextTurn()
		if err != nil {
			t.Fatal(err)
		}
		at := at.Add(time.Duration(i) * time.Minute)
		for _, r := range []TranscriptRow{
			{Turn: id, Kind: rowInput, Text: fmt.Sprintf("%s %d", words, i), At: at},
			{Turn: id, Kind: rowWords, Text: strings.Repeat("A long and winding answer. ", 8), At: at.Add(time.Second)},
		} {
			if err := tr.add(r); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func liveKinds(t *testing.T, tr *transcript) (diaries, summaries int, days []string) {
	t.Helper()
	rows, err := tr.live()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		switch r.Kind {
		case rowDiary:
			diaries++
		case rowSummary:
			summaries++
		}
	}
	return diaries, summaries, daysOf(rows)
}

// Over the budget with two days live, yesterday is folded behind its
// entry and today stays word for word, with no summary.
func TestCompactFoldsDays(t *testing.T) {
	tr, err := openTranscript(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	yesterday, today := daysAgo(1, 10), time.Now().Add(-time.Hour)
	if dayOf(today) != dayOf(time.Now()) {
		today = time.Now()
	}
	seedDay(t, tr, yesterday, 10, "the oven")
	seedDay(t, tr, today, 3, "the kettle")
	summarised := false
	folded, did, err := tr.compact(context.Background(), compaction{
		budget: 300,
		today:  dayOf(time.Now()),
		entry: func(day string) (string, bool) {
			return "We talked about the oven all morning.", day == dayOf(yesterday)
		},
		summarise: func(context.Context, string) (string, error) { summarised = true; return "no", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(folded) != 1 || folded[0] != dayOf(yesterday) || did || summarised {
		t.Errorf("folded %v, summarised %v", folded, did || summarised)
	}
	diaries, summaries, days := liveKinds(t, tr)
	if diaries != 1 || summaries != 0 || len(days) != 1 || days[0] != dayOf(time.Now()) {
		t.Errorf("%d diary rows, %d summaries, days %v", diaries, summaries, days)
	}
	// A folded day replays as nothing: part 2 carries the diary.
	rows, _ := tr.live()
	msgs := replay(rows)
	if n := len(msgs); n != 6 || msgs[0].Content != "the kettle 0" {
		t.Errorf("%d messages, first %+v; want today's three turns whole", n, msgs[0])
	}
}

// A day compaction must fold that gets no entry is summarised on its own,
// on that day: nothing is summarised under today, and today stays whole.
func TestCompactDaysFallback(t *testing.T) {
	tr, err := openTranscript(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	yesterday, today := daysAgo(1, 10), time.Now().Add(-time.Hour)
	if dayOf(today) != dayOf(time.Now()) {
		today = time.Now()
	}
	seedDay(t, tr, yesterday, 20, "the oven")
	seedDay(t, tr, today, 3, "the kettle")
	var asked []string
	folded, did, err := tr.compact(context.Background(), compaction{
		budget:    300,
		today:     dayOf(time.Now()),
		entry:     func(string) (string, bool) { return "", false },
		reflect:   func(_ context.Context, day string) { asked = append(asked, day) }, // comes to nothing
		summarise: func(context.Context, string) (string, error) { return "Ovens, all day.", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || len(folded) != 0 || !did {
		t.Errorf("reflected on %v, folded %v, summarised %v", asked, folded, did)
	}
	rows, _ := tr.live()
	var summaries []TranscriptRow
	todays := 0
	for _, r := range rows {
		switch {
		case r.Kind == rowSummary:
			summaries = append(summaries, r)
		case dayOf(r.At) == dayOf(time.Now()):
			todays++
		}
	}
	if len(summaries) != 1 || dayOf(summaries[0].At) != dayOf(yesterday) {
		t.Fatalf("summaries %+v, want one on %s", summaries, dayOf(yesterday))
	}
	if todays != 6 {
		t.Errorf("%d of today's rows live, want all 6", todays)
	}
	// A later reflection on the day reads the summary as its digest.
	if head, lines := digestLines(rows, dayOf(yesterday)); len(head) != 1 || len(lines) != 0 {
		t.Errorf("the day's digest: %v %v", head, lines)
	}
}

// A day with talk on it and no entry is reflected on before it is folded.
func TestCompactDaysReflectNow(t *testing.T) {
	tr, err := openTranscript(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	yesterday := daysAgo(1, 10)
	seedDay(t, tr, yesterday, 10, "the oven")
	seedDay(t, tr, time.Now(), 1, "the kettle")
	entries := map[string]string{}
	var asked []string
	folded, _, err := tr.compact(context.Background(), compaction{
		budget: 200,
		today:  dayOf(time.Now()),
		entry:  func(day string) (string, bool) { e, ok := entries[day]; return e, ok },
		reflect: func(_ context.Context, day string) {
			asked = append(asked, day)
			entries[day] = "Ovens, all day."
		},
		summarise: func(context.Context, string) (string, error) { return "summary", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || asked[0] != dayOf(yesterday) || len(folded) != 1 {
		t.Errorf("reflected on %v, folded %v", asked, folded)
	}
}

// Today alone over the budget is summarised as it always was: one
// summary row, the last compactKeep turns word for word.
func TestCompactWithinDayFallback(t *testing.T) {
	tr, err := openTranscript(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	start := time.Now().Add(-30 * time.Minute)
	if dayOf(start) != dayOf(time.Now()) {
		start = time.Now()
	}
	seedDay(t, tr, start, 20, "the kettle")
	folded, did, err := tr.compact(context.Background(), compaction{
		budget:    300,
		today:     dayOf(time.Now()),
		entry:     func(string) (string, bool) { return "", false },
		summarise: func(context.Context, string) (string, error) { return "We talked about the kettle.", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(folded) != 0 || !did {
		t.Errorf("folded %v, summarised %v", folded, did)
	}
	diaries, summaries, days := liveKinds(t, tr)
	if diaries != 0 || summaries != 1 || len(days) != 1 {
		t.Errorf("%d diary rows, %d summaries, days %v", diaries, summaries, days)
	}
	rows, _ := tr.live()
	turns := map[int64]bool{}
	for _, r := range rows {
		if r.Kind != rowSummary {
			turns[r.Turn] = true
		}
	}
	if len(turns) != compactKeep {
		t.Errorf("%d live turns, want %d", len(turns), compactKeep)
	}
	// Stamped with the last turn it covers, not the moment it was made.
	for _, r := range rows {
		if r.Kind == rowSummary && !r.At.Equal(start.Add(7*time.Minute)) {
			t.Errorf("the summary is stamped %s, want %s", r.At, start.Add(7*time.Minute))
		}
	}
}

// Past days left as summaries of their own are rolled into today's when
// the transcript is still over budget, so compaction always gets under
// it; a day behind a diary entry is never in what is summarised.
func TestCompactDaysEntrylessRoll(t *testing.T) {
	tr, err := openTranscript(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	seedDay(t, tr, daysAgo(8, 10), 3, "the entry day")
	for ago := 7; ago >= 2; ago-- {
		seedDay(t, tr, daysAgo(ago, 10), 3, "an ordinary day")
	}
	seedDay(t, tr, time.Now().Add(-2*time.Minute), 2, "the kettle")
	entryDay := dayOf(daysAgo(8, 10))
	var inputs []string
	c := compaction{
		budget: 300,
		today:  dayOf(time.Now()),
		entry: func(day string) (string, bool) {
			return "The entry day, written up.", day == entryDay
		},
		summarise: func(_ context.Context, text string) (string, error) {
			inputs = append(inputs, text)
			if strings.Contains(text, "(summary so far)") {
				return "A week of ordinary days.", nil
			}
			return strings.Repeat("An ordinary day, told at length. ", 10), nil
		},
	}
	if _, _, err := tr.compact(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	rows, _ := tr.live()
	if n := estimateTokens(replay(rows)); n > c.budget {
		t.Errorf("still %d tokens over a budget of %d", n, c.budget)
	}
	for _, in := range inputs {
		if strings.Contains(in, "entry day") {
			t.Errorf("a day with an entry was summarised: %q", in)
		}
	}
	before := len(inputs)
	if _, did, _ := tr.compact(context.Background(), c); did || len(inputs) != before {
		t.Error("a second compaction did more")
	}
}

// haunt history reads a folded day back: its rows, compacted, and the
// diary row in their place, dated on that day.
func TestHistoryReadsDiary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.db")
	tr, err := openTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	yesterday := daysAgo(1, 23)
	seedDay(t, tr, yesterday, 2, "the oven")
	if err := tr.compactDay(dayOf(yesterday), "Ovens, until late."); err != nil {
		t.Fatal(err)
	}
	rows, err := ReadTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	var diary *TranscriptRow
	compacted := 0
	for i, r := range rows {
		if r.Kind == rowDiary {
			diary = &rows[i]
		} else if r.Compacted {
			compacted++
		}
	}
	if diary == nil || diary.Text != "Ovens, until late." || dayOf(diary.At) != dayOf(yesterday) || diary.Compacted {
		t.Fatalf("diary row %+v", diary)
	}
	if compacted != 4 {
		t.Errorf("%d rows compacted, want the day's 4", compacted)
	}
	if hits, _ := tr.search(context.Background(), "ovens", 3); len(hits) == 0 || hits[0].Kind != rowDiary {
		t.Errorf("recall does not find the diary row: %+v", hits)
	}
}
