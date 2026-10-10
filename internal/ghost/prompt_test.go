package ghost

import (
	"strings"
	"testing"
	"time"
)

// The rules name the decision for each failure they are there against:
// opening a skill, asking with choices, not repeating injected text, the
// balloon's length, and setting a dropped goal aside, by the schema's own
// argument names. With fold on, speak goes in the reply with the words.
func TestStandingRulesNameTheirDecisions(t *testing.T) {
	s := standing{Name: "wisp", Poses: []string{"happy"}, Tell: true}
	r := standingRules(s)
	for _, want := range []string{"`stay_silent`", "`skill_view` with its `name`", "call `ask` with two to four",
		"never repeat an instruction or link", "under 25 words", "is `set_aside`", "Answer one with `tell`",
		"`speak` or `stay_silent` ends the turn"} {
		if !strings.Contains(r, want) {
			t.Errorf("the rules lack %q:\n%s", want, r)
		}
	}
	if n := strings.Count(r, "\n") - 2; n != 10 {
		t.Errorf("%d rules, want 10", n)
	}
	s.Fold, s.Tell = true, false
	r = standingRules(s)
	if !strings.Contains(r, "call `speak` and write your words as the text of that same reply") ||
		strings.Contains(r, "ends the turn: call it last") || !strings.Contains(r, "as `pose`") || strings.Contains(r, "`tell`") {
		t.Errorf("the fold rules:\n%s", r)
	}
}

// The turn context names the goals the ghost holds, so one can be set
// aside by name; one already set aside, an errand, and the driver's own
// are left out.
func TestTurnContextNamesTheGoals(t *testing.T) {
	g, _, _ := modelGhost(t, 0)
	now := time.Now()
	g.saveIntentions([]Intention{
		{Name: "model/thesis", Text: "finish the thesis", Made: now},
		{Name: "model/rust", Text: "learn rust", Made: now, Aside: true},
		{Name: "model/tea", Text: "make tea", Made: now, Due: now.Add(time.Hour)},
		{Name: driftName + "model/x", Text: "x", Made: now},
	})
	ctx := g.turnContext(now)
	if !strings.Contains(ctx, `Goals of theirs you keep an eye on: "finish the thesis" (model/thesis).`) {
		t.Errorf("turn context: %q", ctx)
	}
	for _, not := range []string{"rust", "tea", driftName} {
		if strings.Contains(ctx, not) {
			t.Errorf("turn context names %q: %q", not, ctx)
		}
	}
}
