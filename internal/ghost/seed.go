package ghost

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// SeedGoal writes a goal into the store of a ghost that is not running, as
// though the person had asked for it age ago and it had never been on
// screen since. It exists so that what a ghost does with an old goal can
// be watched in a sitting (scripts/spinseed) and graded (the evals in
// cmd/haunt), instead of a week later: the goal check wants a goal held
// for goalMinAge and unseen for driftAfter before it says anything.
//
// It writes the store directly, which is only safe while no ghost holds
// it. Watch nil takes the goal's own words, as intend does. A goal already
// held with the same text is replaced rather than doubled, so seeding
// twice is seeding once; anything else held is kept.
func SeedGoal(s Store, text string, watch []string, age time.Duration) error {
	var list []Intention
	raw, ok, err := s.Get(intentionsKey)
	if err != nil {
		return err
	}
	if ok {
		if list, err = DecodeIntentions(raw); err != nil {
			return fmt.Errorf("the intentions in the store are corrupt: %w", err)
		}
	}
	if watch == nil {
		watch = watchWords(text)
	}
	goal := Intention{
		Name:  seedName(list),
		Text:  text,
		Made:  time.Now().Add(-age),
		Watch: watch,
		Asked: true,
	}
	replaced := false
	for i, it := range list {
		if it.Text == text {
			goal.Name = it.Name
			list[i], replaced = goal, true
		}
	}
	if !replaced {
		list = append(list, goal)
	}
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return s.Set(intentionsKey, string(b))
}

// seedName is a name no intention in list has.
func seedName(list []Intention) string {
	for n := len(list) + 1; ; n++ {
		name := fmt.Sprintf("%sseeded-%d", hermesIntentionPrefix, n)
		if !slices.ContainsFunc(list, func(it Intention) bool { return it.Name == name }) {
			return name
		}
	}
}
