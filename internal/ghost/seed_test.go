package ghost

import (
	"slices"
	"testing"
	"time"
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
	list, err := DecodeIntentions(s.m[intentionsKey])
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
