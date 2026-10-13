package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/ghost"
)

// The transcript's lines group into turns, each final reply takes its own
// reasons, and a turn older than the reasons' ring is still recorded, from
// the transcript alone.
func TestHistoryTurns(t *testing.T) {
	at := time.Unix(1789614336, 0)
	msgs := []message{
		// The tail of a turn the window cut into.
		{Role: "assistant", Text: "…and that was that.", Time: at},
		{Role: "user", Text: ghost.Envelope("idle"), Time: at},
		{Role: "assistant", Silent: true, Time: at},
		{Role: "user", Text: "what's on my list?", Time: at},
		{Role: "assistant", Text: "Let me look.", Interim: true, Time: at},
		{Role: "assistant", Text: "Eggs, and the oven.", Time: at.Add(time.Second),
			Acted:  []call{{Tool: "notes__read", Args: `{"file":"list"}`, Result: "eggs\noven"}},
			Looked: []call{{Tool: "skill_view", Args: `{"name":"shopping-list"}`, Result: "…"}}},
	}
	// Only the last turn is still in the ring, noted just before its row
	// was written. The idle turn's silent reply is a second away and would
	// do by time alone, but the closer reply takes it.
	traces := []ghost.Why{{Time: at.Add(time.Second - 5*time.Millisecond), User: "what's on my list?", Reply: "Eggs, and the oven.",
		Acted: []string{"notes__read"}, Window: ghost.WhyWindow{App: "foot", Title: "~"}}}

	turns := mergeTurns(msgs, traces)
	if len(turns) != 3 || turns[0].input != nil || turns[1].final() == nil || len(turns[2].replies) != 2 {
		t.Fatalf("turns %+v", turns)
	}
	if turns[1].whyKept || !turns[2].whyKept {
		t.Errorf("reasons matched to the wrong turns: %+v", turns)
	}

	idle := turns[1].record()
	if idle.ProvokedBy != "event" || idle.Input != "kind=idle" || !idle.Silent || idle.Reply != "" ||
		!idle.SelfPrompt || idle.Window != nil || !idle.Time.Equal(at) || idle.Acted == nil || idle.Looked == nil {
		t.Errorf("idle turn, older than the ring: %+v", idle)
	}
	list := turns[2].record()
	if list.ProvokedBy != "person" || list.Silent || list.SelfPrompt || list.Reply != "Let me look.\nEggs, and the oven." ||
		list.Window == nil || list.Window.App != "foot" || !list.Time.Equal(at.Add(time.Second)) {
		t.Errorf("list turn %+v", list)
	}
	failed := historyTurn{input: &message{Role: "user", Text: "hello?", Time: at},
		replies: []message{{Role: "assistant", Failed: true, Time: at}}}.record()
	if !failed.Failed || failed.Silent || failed.Reply != "" {
		t.Errorf("failed turn %+v", failed)
	}
	if len(list.Acted) != 1 || list.Acted[0] != (traceCall{"notes__read", `{"file":"list"}`, "eggs\noven"}) ||
		len(list.Looked) != 1 || list.Looked[0].Tool != "skill_view" {
		t.Errorf("list turn calls %+v %+v", list.Acted, list.Looked)
	}
}

// Reasons go to replies by time, not words: of two silent turns, the one
// whose reasons were kept gets them and the other does not take them for
// having the same (empty) words, nor do two identical replies cross. A
// reply with none says whether the ring has dropped them or never had
// them.
func TestHistoryWhyByTime(t *testing.T) {
	at := time.Unix(1789614336, 0)
	ms := time.Millisecond
	msgs := []message{
		// Before the oldest reasons the ring holds.
		{Role: "user", Text: "morning", Time: at},
		{Role: "assistant", Text: "Morning.", Time: at.Add(time.Second)},
		// A silent turn with reasons, then one without (a reflection that
		// kept none), then two that said the same thing.
		{Role: "user", Text: ghost.Envelope("idle"), Time: at.Add(time.Minute)},
		{Role: "assistant", Silent: true, Time: at.Add(time.Minute + 2*time.Second)},
		{Role: "user", Text: ghost.Envelope("reflect"), Time: at.Add(2 * time.Minute)},
		{Role: "assistant", Silent: true, Time: at.Add(2*time.Minute + time.Second)},
		{Role: "user", Text: "hello", Time: at.Add(3 * time.Minute)},
		{Role: "assistant", Text: "Hi.", Time: at.Add(3*time.Minute + time.Second)},
		{Role: "user", Text: "hello again", Time: at.Add(4 * time.Minute)},
		{Role: "assistant", Text: "Hi.", Time: at.Add(4*time.Minute + time.Second)},
	}
	traces := []ghost.Why{
		{Time: at.Add(time.Minute + 2*time.Second - 3*ms), User: ghost.Envelope("idle"), SelfPrompt: true, Suppressed: true},
		{Time: at.Add(3*time.Minute + time.Second - 3*ms), User: "hello", Reply: "Hi.", Window: ghost.WhyWindow{App: "foot"}},
		{Time: at.Add(4*time.Minute + time.Second - 3*ms), User: "hello again", Reply: "Hi.", Window: ghost.WhyWindow{App: "firefox"}},
	}
	turns := mergeTurns(msgs, traces)
	if len(turns) != 5 {
		t.Fatalf("turns %+v", turns)
	}
	if turns[0].whyKept || !turns[0].whyGone {
		t.Errorf("the oldest turn: kept %v gone %v", turns[0].whyKept, turns[0].whyGone)
	}
	if !turns[1].whyKept || !turns[1].why.Suppressed {
		t.Errorf("the idle turn's reasons: %+v", turns[1])
	}
	if turns[2].whyKept || turns[2].whyGone {
		t.Errorf("the reflection: kept %v gone %v", turns[2].whyKept, turns[2].whyGone)
	}
	if turns[3].why.User != "hello" || turns[4].why.User != "hello again" {
		t.Errorf("identical replies crossed: %q %q", turns[3].why.User, turns[4].why.User)
	}

	var out bytes.Buffer
	if err := showHistory(&out, msgs, traces, 50, true, false); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	gone := "(no reasons kept for this one — it is older than the trace)"
	none := "(no reasons recorded for this one)"
	if strings.Count(got, gone) != 1 || strings.Count(got, none) != 1 ||
		strings.Index(got, gone) > strings.Index(got, "event: kind=idle") ||
		strings.Index(got, none) < strings.Index(got, "event: kind=reflect") ||
		strings.Index(got, none) > strings.Index(got, "you:   hello") {
		t.Errorf("the messages are not where they belong:\n%s", got)
	}
}

// A compacted transcript reads back whole: the older turns, the summary
// where it was made, as summary:, and the turns since. -json leaves the
// summary out, since it is not a turn.
func TestCompactionHistory(t *testing.T) {
	at := time.Unix(1789614336, 0)
	var rows []ghost.TranscriptRow
	for i := int64(1); i <= 3; i++ {
		rows = append(rows,
			ghost.TranscriptRow{Turn: i, Kind: "input", Text: "turn " + string(rune('0'+i)), At: at, Compacted: i < 3},
			ghost.TranscriptRow{Turn: i, Kind: "words", Text: "ok " + string(rune('0'+i)), At: at, Compacted: i < 3})
		if i == 2 {
			rows = append(rows, ghost.TranscriptRow{Turn: 2, Kind: "summary", Text: "We counted to two.", At: at})
		}
	}
	msgs := messagesOf(rows)
	var out bytes.Buffer
	if err := showHistory(&out, msgs, nil, 50, false, false); err != nil {
		t.Fatal(err)
	}
	want := "you:   turn 1\nghost: ok 1\nyou:   turn 2\nghost: ok 2\nsummary: We counted to two.\nyou:   turn 3\nghost: ok 3\n"
	if out.String() != want {
		t.Errorf("history:\n%s\nwant\n%s", out.String(), want)
	}
	out.Reset()
	if err := showHistory(&out, msgs, nil, 50, false, true); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out.String(), "\n"); n != 3 || strings.Contains(out.String(), "counted") {
		t.Errorf("-json:\n%s", out.String())
	}
}

// A day folded behind its diary entry reads back as diary <day>: where
// the day was, after its turns; -json leaves it out, as a summary.
func TestHistoryDiary(t *testing.T) {
	day := time.Date(2026, 10, 11, 21, 0, 0, 0, time.Local)
	next := day.Add(14 * time.Hour)
	rows := []ghost.TranscriptRow{
		{Turn: 1, Kind: "input", Text: "the oven", At: day, Compacted: true},
		{Turn: 1, Kind: "words", Text: "Hot.", At: day, Compacted: true},
		{Turn: 1, Kind: "diary", Text: "We talked about the oven.", At: day},
		{Turn: 2, Kind: "input", Text: "morning", At: next},
		{Turn: 2, Kind: "words", Text: "Morning.", At: next},
	}
	msgs := messagesOf(rows)
	var out bytes.Buffer
	if err := showHistory(&out, msgs, nil, 50, false, false); err != nil {
		t.Fatal(err)
	}
	want := "you:   the oven\nghost: Hot.\ndiary 2026-10-11: We talked about the oven.\nyou:   morning\nghost: Morning.\n"
	if out.String() != want {
		t.Errorf("history:\n%s\nwant\n%s", out.String(), want)
	}
	out.Reset()
	if err := showHistory(&out, msgs, nil, 50, false, true); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out.String(), "\n"); n != 2 || strings.Contains(out.String(), "oven.") {
		t.Errorf("-json:\n%s", out.String())
	}
}
