package ghost

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/store"
)

// A seeded goal is one the goal check finds gone quiet at once, which is
// the whole of what seeding is for; a second goal is added beside it, and
// seeding the same goal again replaces it.
func TestSeedGoal(t *testing.T) {
	s := &memStore{m: map[string]string{}}
	if err := SeedGoal(s, "finish the thesis", nil, 8*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := SeedGoal(s, "learn the cello", []string{"cello"}, 10*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := SeedGoal(s, "finish the thesis", nil, 9*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	list, err := DecodeIntentions(s.m[IntentionsStoreKey])
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("intentions = %+v, want the two goals", list)
	}
	thesis, cello := list[0], list[1]
	if thesis.Text != "finish the thesis" || !slices.Contains(thesis.Watch, "thesis") {
		t.Errorf("thesis = %+v, want its own words to watch for", thesis)
	}
	if !cello.Goal() || !cello.Asked || !slices.Equal(cello.Watch, []string{"cello"}) {
		t.Errorf("cello = %+v, want a goal asked for, watching for the given word", cello)
	}
	if thesis.Name == cello.Name {
		t.Errorf("both goals are named %q", thesis.Name)
	}
	if age := time.Since(thesis.Made); age < 9*24*time.Hour-time.Minute || age > 9*24*time.Hour+time.Minute {
		t.Errorf("the reseeded thesis was made %s ago, want nine days", age)
	}
	if i, ok := drifted(list, time.Now()); !ok {
		t.Error("the goal check finds no seeded goal gone quiet")
	} else if list[i].Text != "learn the cello" {
		t.Errorf("gone quiet: %q, want the older goal first", list[i].Text)
	}
}

// Seeded rows are the transcript's own: live, numbered after what is
// there, dated as given, and searchable like any other.
func TestSeedTranscript(t *testing.T) {
	dir := t.TempDir()
	at := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	rows := []TranscriptRow{
		{Turn: 1, Kind: rowInput, Text: "I moved to Brisbane", At: at},
		{Turn: 1, Kind: rowWords, Text: "Noted.", At: at.Add(time.Second)},
	}
	if err := SeedTranscript(dir, rows); err != nil {
		t.Fatal(err)
	}
	if err := SeedTranscript(dir, rows); err != nil {
		t.Fatal(err)
	}
	if err := SeedTranscript(dir, []TranscriptRow{{Kind: "spoken"}}); err == nil {
		t.Error("a row of no known kind was seeded")
	}
	got, err := ReadTranscript(filepath.Join(dir, "transcript.db"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[0].Turn != 1 || got[2].Turn != 2 || !got[0].At.Equal(at) || got[3].Text != "Noted." || got[0].Compacted {
		t.Fatalf("rows = %+v", got)
	}
	tr, err := openTranscript(filepath.Join(dir, "transcript.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	if hits, err := tr.search(t.Context(), "Brisbane", 5); err != nil || len(hits) != 2 {
		t.Errorf("search = %+v, %v; want both seeded inputs", hits, err)
	}
	if when, ok := tr.lastExchange(); !ok || !when.Equal(at.Add(time.Second)) {
		t.Errorf("lastExchange = %v, %v", when, ok)
	}
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, met, _ := st.Get(metKey); !met {
		t.Error("a ghost with a transcript has not booted before")
	}
}
