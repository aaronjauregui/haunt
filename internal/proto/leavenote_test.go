package proto

import (
	"strings"
	"testing"
)

// A note holds a few words: empty is refused, and so is one past the cap,
// counted in characters rather than bytes so a note in another script is
// not cut shorter than one in English.
func TestLeaveNoteValidation(t *testing.T) {
	for _, c := range []struct {
		name, text string
		ok         bool
	}{
		{"words", "water the plants", true},
		{"empty", "", false},
		{"blank", "   ", false},
		{"at the cap", strings.Repeat("a", MaxNoteChars), true},
		{"past the cap", strings.Repeat("a", MaxNoteChars+1), false},
		{"wide runes at the cap", strings.Repeat("水", MaxNoteChars), true},
	} {
		in := Intent{Type: IntentLeaveNote, Text: c.text}
		if err := in.Validate(); (err == nil) != c.ok {
			t.Errorf("%s: Validate() = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}
