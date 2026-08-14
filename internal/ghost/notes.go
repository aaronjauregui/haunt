package ghost

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// notesKey holds the ghost's journal: self-authored observations that shape
// future behavior. Distinct from KV facts — notes are prose, timestamped,
// and injected into LLM context, so what the ghost chooses to write down
// changes who it becomes.
const notesKey = internalKeyPrefix + "notes"

// Note is one journal entry.
type Note struct {
	Time time.Time `json:"time"`
	Text string    `json:"text"`
}

const (
	maxNotes       = 200
	notesInContext = 20
	maxNoteLength  = 500
)

// execNote appends a self-note to the journal.
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
	if err := g.store.Set(notesKey, string(b)); err != nil {
		g.opts.Log(fmt.Sprintf("haunt: saving note: %v", err))
		return
	}
	// Notes age out of the recency window after 20 more; the memory index
	// is how one from a year ago can still surface. Off the intent path —
	// unlike chat, execNote runs inline, and an embedding backend that
	// hangs must not take the intent loop with it. aiMu keeps the ring
	// cursor to one writer.
	if g.memoryEnabled() {
		go func() {
			g.aiMu.Lock()
			defer g.aiMu.Unlock()
			g.remember("note", text)
		}()
	}
}

func (g *Ghost) loadNotes() []Note {
	raw, ok, err := storeGet(g.store, notesKey)
	if err != nil || !ok {
		return nil
	}
	var n []Note
	if json.Unmarshal([]byte(raw), &n) != nil {
		return nil
	}
	return n
}

// notesContext renders recent notes for the LLM system context, and returns
// the set of texts it included so similarity recall (memory.go) can spend
// its slots on things recency did not already cover.
func (g *Ghost) notesContext() (string, map[string]bool) {
	notes := g.loadNotes()
	if len(notes) == 0 {
		return "", nil
	}
	if len(notes) > notesInContext {
		notes = notes[len(notes)-notesInContext:]
	}
	included := make(map[string]bool, len(notes))
	var b strings.Builder
	b.WriteString("\n\nYour private journal (things you chose to remember; let them shape you):\n")
	for _, n := range notes {
		fmt.Fprintf(&b, "- [%s] %s\n", n.Time.Format("Jan 2"), n.Text)
		included[n.Text] = true
	}
	return b.String(), included
}

// DecodeNotes parses a stored journal value; used by the journal CLI.
func DecodeNotes(raw string) ([]Note, error) {
	var n []Note
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		return nil, err
	}
	return n, nil
}

// NotesStoreKey exposes the journal's store key to the CLI.
const NotesStoreKey = notesKey
