package ghost

import (
	"encoding/json"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
)

// NotesStoreKey holds the ghost's journal: the action log. Each entry is a line
// the driver or a scripted brain wrote about something the ghost did — a
// tool it used, a goal it took on — so `haunt journal` can show the person
// what happened. It is not a prompt block: a ghost on a model keeps what it
// remembers in its memory files (internal/memory). Exported for `haunt
// journal`.
const NotesStoreKey = internalKeyPrefix + "notes"

// Note is one journal entry.
type Note struct {
	Time time.Time `json:"time"`
	Text string    `json:"text"`
}

const (
	maxNotes      = 200
	maxNoteLength = 500
)

// execNote appends one entry to the journal.
func (g *Ghost) execNote(text string) {
	if len(text) > maxNoteLength {
		text = text[:maxNoteLength]
	}
	notes := g.loadNotes()
	notes = append(notes, Note{Time: time.Now(), Text: text})
	if len(notes) > maxNotes {
		notes = notes[len(notes)-maxNotes:]
	}
	b, err := json.Marshal(notes)
	if err != nil {
		return
	}
	if err := g.store.Set(NotesStoreKey, string(b)); err != nil {
		g.log.Warn(hlog.Memory, "saving note: %v", err)
	}
}

func (g *Ghost) loadNotes() []Note {
	raw, ok, err := g.store.Get(NotesStoreKey)
	if err != nil || !ok {
		return nil
	}
	var n []Note
	if json.Unmarshal([]byte(raw), &n) != nil {
		return nil
	}
	return n
}

// DecodeNotes parses a stored journal value; used by the journal CLI.
func DecodeNotes(raw string) ([]Note, error) {
	var n []Note
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		return nil, err
	}
	return n, nil
}
