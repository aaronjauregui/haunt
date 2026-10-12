package ghost

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/aaronjauregui/haunt/internal/store"
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
	raw, ok, err := s.Get(IntentionsStoreKey)
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
	return s.Set(IntentionsStoreKey, string(b))
}

// seedName is a name no intention in list has.
func seedName(list []Intention) string {
	for n := len(list) + 1; ; n++ {
		name := fmt.Sprintf("%sseeded-%d", modelIntentionPrefix, n)
		if !slices.ContainsFunc(list, func(it Intention) bool { return it.Name == name }) {
			return name
		}
	}
}

// SeedTranscript appends rows to the transcript of a ghost that is not
// running, whose state directory is stateDir, as though those turns had
// happened when their At says. It exists so that what a ghost does with
// days gone by (the Last exchange line, the returned turn, reflection
// and the diary) can be graded in a sitting by the evals in cmd/haunt,
// instead of over a week. The rows are ordinary rows: nothing in the
// ghost can tell a seeded turn from one it had.
//
// Each row's Turn is counted from the transcript's last turn, so rows
// numbered from 1 follow whatever is there already. A row with no At
// takes now. Kind must be one of the transcript's row kinds ("input",
// "event", "calls", "result", "words", "silent", "failed", "summary",
// "diary"). A ghost that has talked has booted before, so its store in
// stateDir (state.db) is marked as its first boot would have marked it,
// and the next boot is not its first. Like SeedGoal, it is only safe
// while no ghost holds either file.
func SeedTranscript(stateDir string, rows []TranscriptRow) error {
	kinds := []string{rowInput, rowEvent, rowCalls, rowResult, rowWords, rowSilent, rowFailed, rowSummary, rowDiary}
	for _, r := range rows {
		if !slices.Contains(kinds, r.Kind) {
			return fmt.Errorf("seeding the transcript: no row kind %q", r.Kind)
		}
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	tr, err := openTranscript(filepath.Join(stateDir, "transcript.db"))
	if err != nil {
		return err
	}
	defer tr.Close()
	next, err := tr.nextTurn()
	if err != nil {
		return err
	}
	for _, r := range rows {
		r.Turn += next - 1
		if err := tr.add(r); err != nil {
			return fmt.Errorf("seeding the transcript: %w", err)
		}
	}
	st, err := store.Open(filepath.Join(stateDir, "state.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	return st.Set(metKey, "yes")
}
