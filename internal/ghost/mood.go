package ghost

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
)

// Mood: one short line the ghost sets about how it is feeling, so a
// feeling it arrived at in one turn colours the turns after it instead of
// being forgotten with the transcript's next summary.
//
// It is kept in the store, so a restart keeps it, and it carries the time
// it was set, so it clears with the day without a timer: the turn context
// renders it only while it was set today, and a mood from yesterday is
// simply not there in the morning. It is the model's own bookkeeping and
// nothing more: it is not shown in the balloon and the face at rest does
// not follow it. The person sees it with `haunt journal -mood`.

// MoodStoreKey holds the ghost's mood. Exported for `haunt journal -mood`.
const MoodStoreKey = internalKeyPrefix + "mood"

// maxMoodChars is the longest mood, in characters: one short line, since
// it rides in every turn's context.
const maxMoodChars = 120

// Mood is the line the ghost set and when.
type Mood struct {
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// DecodeMood parses a stored mood; used by the journal CLI.
func DecodeMood(raw string) (Mood, error) {
	var m Mood
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return Mood{}, err
	}
	return m, nil
}

// Today says the mood was set on now's calendar day, in now's zone, which
// is while the ghost still has it in mind.
func (m Mood) Today(now time.Time) bool {
	at := m.At.In(now.Location())
	return m.Text != "" && at.Year() == now.Year() && at.YearDay() == now.YearDay()
}

// mood is the stored mood, if there is one, whatever day it was set.
func (g *Ghost) mood() (Mood, bool) {
	if g.store == nil {
		return Mood{}, false
	}
	raw, ok, err := g.store.Get(MoodStoreKey)
	if err != nil || !ok {
		return Mood{}, false
	}
	m, err := DecodeMood(raw)
	if err != nil || m.Text == "" {
		return Mood{}, false
	}
	return m, true
}

// execMood replaces the mood with text, set at now. The text is made one
// line; empty or longer than maxMoodChars is refused, with the reason for
// the model to read.
func (g *Ghost) execMood(text string, now time.Time) (Mood, error) {
	text = strings.Join(strings.Fields(text), " ")
	switch n := len([]rune(text)); {
	case n == 0:
		return Mood{}, fmt.Errorf("say how you are feeling, in a few words")
	case n > maxMoodChars:
		return Mood{}, fmt.Errorf("a mood is one short line, under %d characters; that was %d", maxMoodChars, n)
	}
	m := Mood{Text: text, At: now}
	b, err := json.Marshal(m)
	if err != nil {
		return Mood{}, err
	}
	if err := g.store.Set(MoodStoreKey, string(b)); err != nil {
		g.log.Warn(hlog.Store, "saving the mood: %v", err)
		return Mood{}, fmt.Errorf("it could not be kept")
	}
	return m, nil
}

// moodLine is the turn context's line for the mood, empty when there is
// none or it was set on an earlier day.
func (g *Ghost) moodLine(now time.Time) string {
	m, ok := g.mood()
	if !ok || !m.Today(now) {
		return ""
	}
	return "Your mood: " + m.Text
}
