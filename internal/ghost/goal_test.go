package ghost

import (
	"strings"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/proto"
)

func TestWatchWordsKeepTheSubject(t *testing.T) {
	cases := []struct {
		text string
		want []string
	}{
		{"the thesis matters", []string{"thesis"}},
		{"remind me to keep an eye on the thesis chapter", []string{"thesis", "chapter"}},
		// Nothing survives, and nothing is invented: a goal that names no
		// subject is a goal the ghost cannot honestly watch.
		{"I should be better about this", []string{"better"}},
		{"keep at it more", nil},
		// Repetition is one word, and the list is bounded.
		{"rust rust rust", []string{"rust"}},
	}
	for _, c := range cases {
		got := watchWords(c.text)
		if len(got) != len(c.want) {
			t.Errorf("watchWords(%q) = %v, want %v", c.text, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("watchWords(%q) = %v, want %v", c.text, got, c.want)
				break
			}
		}
	}
}

// The record-time filter, which is the whole privacy argument: what is
// written down is that a goal the person named was in front of them, and
// when. Nothing about the window survives the call.
func TestSightingsRecordOnlyTheGoal(t *testing.T) {
	g, _, s := intendingGhost(t)
	intend(g, "thesis", "the thesis matters", 0)
	now := time.Now()

	g.noteGoalSighting("nvim", "thesis.tex — chapter 3", now)
	held := g.loadIntentions()
	if len(held[0].Seen) != 1 {
		t.Fatalf("recorded %d sightings, want 1", len(held[0].Seen))
	}
	if !held[0].LastSeen().Equal(now) {
		t.Errorf("last seen %v, want %v", held[0].LastSeen(), now)
	}
	// The title is not in the store anywhere, under any key.
	for k, v := range s.m {
		if strings.Contains(v, "chapter 3") {
			t.Fatalf("the window title was written down under %s: %s", k, v)
		}
	}

	// A window that has nothing to do with the goal is not recorded at all.
	g.noteGoalSighting("firefox", "cat pictures", now.Add(time.Hour))
	if held := g.loadIntentions(); len(held[0].Seen) != 1 {
		t.Errorf("recorded %d sightings, want an unrelated window ignored", len(held[0].Seen))
	}
}

// The poll runs every three seconds. An afternoon at the thesis is one
// fact, not four thousand.
func TestSightingsAreRationed(t *testing.T) {
	g, _, _ := intendingGhost(t)
	intend(g, "thesis", "the thesis matters", 0)
	start := time.Now()
	for i := range 200 {
		g.noteGoalSighting("nvim", "thesis.tex", start.Add(time.Duration(i)*3*time.Second))
	}
	held := g.loadIntentions()
	// Ten minutes of it at one every five minutes, plus the first.
	if n := len(held[0].Seen); n < 2 || n > 4 {
		t.Errorf("recorded %d sightings across ten minutes, want a handful", n)
	}
}

// A goal only says anything once there is a fortnight behind it. Day one is
// silent by design and so is day six: a ghost that passes judgement the
// moment a goal is stated was watching before it was asked.
func TestAGoalIsSilentAtFirst(t *testing.T) {
	now := time.Now()
	fresh := Intention{Name: "a", Text: "the thesis matters", Made: now.Add(-2 * 24 * time.Hour), Watch: []string{"thesis"}}
	if _, ok := drifted([]Intention{fresh}, now); ok {
		t.Error("a goal two days old produced a verdict")
	}
	old := fresh
	old.Made = now.Add(-30 * 24 * time.Hour)
	if _, ok := drifted([]Intention{old}, now); !ok {
		t.Error("a goal held a month and never seen produced nothing")
	}
}

func TestDriftIgnoresWhatIsBeingWorkedOn(t *testing.T) {
	now := time.Now()
	it := Intention{
		Name: "a", Text: "the thesis matters", Made: now.Add(-30 * 24 * time.Hour),
		Watch: []string{"thesis"}, Seen: []time.Time{now.Add(-2 * 24 * time.Hour)},
	}
	if _, ok := drifted([]Intention{it}, now); ok {
		t.Error("a goal touched two days ago was reported as neglected")
	}
}

// Having said it once, it does not say it again for a good long while.
// Nagging is the failure mode this can most easily have.
func TestDriftDoesNotNag(t *testing.T) {
	now := time.Now()
	it := Intention{
		Name: "a", Text: "the thesis matters", Made: now.Add(-90 * 24 * time.Hour),
		Watch: []string{"thesis"}, Said: now.Add(-3 * 24 * time.Hour),
	}
	if _, ok := drifted([]Intention{it}, now); ok {
		t.Error("it brought the same goal up again three days later")
	}
	it.Said = now.Add(-30 * 24 * time.Hour)
	if _, ok := drifted([]Intention{it}, now); !ok {
		t.Error("it never brought the goal up again at all")
	}
}

// At most one per pass: two observations at once is a status report, and a
// ghost that delivers status reports is a dashboard with a face.
func TestAtMostOneGoalPerPass(t *testing.T) {
	now := time.Now()
	list := []Intention{
		{Name: "a", Text: "the thesis matters", Made: now.Add(-40 * 24 * time.Hour), Watch: []string{"thesis"}},
		{Name: "b", Text: "the garden matters", Made: now.Add(-90 * 24 * time.Hour), Watch: []string{"garden"}},
	}
	i, ok := drifted(list, now)
	if !ok {
		t.Fatal("nothing was found among two neglected goals")
	}
	// The one held longest, since that is the one most obviously never
	// acted on.
	if list[i].Name != "b" {
		t.Errorf("picked %q, want the older goal", list[i].Name)
	}
}

// The pass runs while nobody is there, so what it produces waits for
// somebody to raise it with rather than being said to an empty room.
func TestADriftFindingWaitsForSomebody(t *testing.T) {
	g, _, _ := intendingGhost(t)
	intend(g, "thesis", "the thesis matters", 0)
	held := g.loadIntentions()
	held[0].Made = time.Now().Add(-40 * 24 * time.Hour)
	g.saveIntentions(held)

	g.checkGoalsOnce()

	var found *Intention
	for _, it := range g.loadIntentions() {
		if strings.HasPrefix(it.Name, driftName) {
			found = &it
		}
	}
	if found == nil {
		t.Fatal("the check found nothing about a goal untouched for six weeks")
	}
	if !strings.Contains(found.Text, "thesis") {
		t.Errorf("finding = %q, want it to name the goal", found.Text)
	}
	// Due, and deliberately not armed: nobody is there.
	if found.Due.IsZero() {
		t.Error("the finding has no moment, so nothing will ever raise it")
	}
	g.timersMu.Lock()
	_, armed := g.intentTimers[found.Name]
	g.timersMu.Unlock()
	if armed {
		t.Error("the finding was armed while the person was away")
	}

	// And it is armed when they come back.
	g.armLateIntentions()
	g.timersMu.Lock()
	_, armed = g.intentTimers[found.Name]
	g.timersMu.Unlock()
	if !armed {
		t.Error("the finding was not raised when the person came back")
	}
	g.cancelIntention(found.Name)

	// Once said, it stays quiet: a second pass finds nothing.
	before := len(g.loadIntentions())
	g.checkGoalsOnce()
	if after := len(g.loadIntentions()); after != before {
		t.Errorf("a second pass produced another finding (%d then %d)", before, after)
	}
}

// A finding about something the person put on the books is an obligation,
// and one about a goal the brain invented for itself is not. The bit is
// inherited from the goal, where the driver decided it — a brain cannot
// mint itself an allowance by inventing goals while nobody is watching.
func TestAFindingInheritsWhoAskedForIt(t *testing.T) {
	for _, asked := range []bool{true, false} {
		g, _, _ := intendingGhost(t)
		if asked {
			g.markPrompted()
		}
		intend(g, "thesis", "the thesis matters", 0)
		held := g.loadIntentions()
		held[0].Made = time.Now().Add(-40 * 24 * time.Hour)
		g.saveIntentions(held)
		g.checkGoalsOnce()

		for _, it := range g.loadIntentions() {
			if strings.HasPrefix(it.Name, driftName) && it.Asked != asked {
				t.Errorf("finding Asked = %v, want %v — inherited from the goal", it.Asked, asked)
			}
		}
	}
}

// The other exit. A goal given up on is not discharged — saying it was
// would be a lie in the journal — it just stops being watched.
func TestSettingAGoalAside(t *testing.T) {
	g, _, _ := intendingGhost(t)
	intend(g, "thesis", "the thesis matters", 0)
	g.noteGoalSighting("nvim", "thesis.tex", time.Now())

	g.execSetAside("thesis")

	held := g.loadIntentions()
	if len(held) != 1 {
		t.Fatalf("holding %d, want it still on the books rather than discharged", len(held))
	}
	if !held[0].Aside {
		t.Fatal("it is not marked as set aside")
	}
	if len(held[0].Seen) != 0 {
		t.Error("it is still keeping the evidence for a check that will not run")
	}
	if held[0].Watched() {
		t.Error("it is still being watched")
	}
	// And the ghost, asked what it holds, says it was set aside.
	if list, _ := toolIntentions(g, nil, nil); !strings.Contains(list, "(set aside)") {
		t.Errorf("intentions = %q, want the goal marked as set aside", list)
	}
	// And produces nothing, however long it sits there.
	held[0].Made = time.Now().Add(-365 * 24 * time.Hour)
	if _, ok := drifted(held, time.Now()); ok {
		t.Error("a goal set aside still produced a verdict")
	}
}

// Goals have a much smaller cap than errands, and a different argument
// behind it: twelve errands is a busy afternoon, and eight goals is
// somebody who has not chosen.
func TestGoalsAreCappedSeparately(t *testing.T) {
	g, _, _ := intendingGhost(t)
	for i := range maxGoals + 3 {
		intend(g, string(rune('a'+i)), "subject"+string(rune('a'+i)), 0)
		time.Sleep(time.Millisecond)
	}
	held := g.loadIntentions()
	if len(held) != maxGoals {
		t.Fatalf("holding %d goals, want the cap of %d", len(held), maxGoals)
	}
	if held[0].Name != string(rune('a'+3)) {
		t.Errorf("kept from %q, want the oldest three to have gone", held[0].Name)
	}
}

// Restating a goal is a person saying the same thing again, not a fresh
// start — otherwise rephrasing it would buy a silent fortnight.
func TestRestatingAGoalKeepsWhatItKnows(t *testing.T) {
	g, _, _ := intendingGhost(t)
	intend(g, "thesis", "the thesis matters", 0)
	seen := time.Now()
	g.noteGoalSighting("nvim", "thesis.tex", seen)

	g.execIntend(proto.Intent{Type: proto.IntentIntend, Name: "thesis", Text: "the thesis chapter matters"})

	held := g.loadIntentions()
	if len(held[0].Seen) != 1 || !held[0].LastSeen().Equal(seen) {
		t.Errorf("evidence = %v, want it carried across the restatement", held[0].Seen)
	}
	if len(held[0].Watch) != 2 {
		t.Errorf("watching %v, want the new words", held[0].Watch)
	}
}
