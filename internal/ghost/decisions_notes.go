package ghost

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aaronjauregui/haunt/internal/proto"
)

// Notes: a few words left on the desktop to be found later (desknote.go).
// Free as a line is: nothing to declare and no allowance spent. It writes
// something that outlives the turn, so it is a side effect, and is put to
// the person after the turn has read somebody else's words, as `intend` is.

func init() {
	register(verb{
		id: "leave_note",
		desc: fmt.Sprintf("Leave a note on their desktop, to be found later: a reminder, a kind word, at most %d "+
			"characters. It stays until they click it away or you resolve it. picture: the id of a picture you "+
			"painted, to pin above the words. Returns its name.", maxNoteChars),
		params:     []string{"text:string", "picture?:string"},
		run:        tool(toolLeaveNote),
		sideEffect: true,
	})
	// The ghost's own bookkeeping, like intend: gated after reading on a
	// cloud model and nowhere else. Unclassed, it would be treated as
	// reaching past the person and asked about everywhere.
	verbClasses["leave_note"] = classKeep
}

func toolLeaveNote(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Text    string `json:"text"`
		Picture string `json:"picture"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	name, err := g.execLeaveNote(proto.Intent{Type: proto.IntentLeaveNote, Text: a.Text, Name: a.Picture})
	if err != nil {
		return "", err
	}
	return "Left " + name + " on their desktop.", nil
}
