package ghost

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"strings"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/model/fake"
	"github.com/aaronjauregui/haunt/internal/proto"
)

func leaveNote(g *Ghost, text, picture string) (string, error) {
	return g.execLeaveNote(proto.Intent{Type: proto.IntentLeaveNote, Text: text, Name: picture})
}

func notesIn(list []Intention) []Intention {
	var out []Intention
	for _, it := range list {
		if it.Note != nil {
			out = append(out, it)
		}
	}
	return out
}

// leave_note is a decision like any other: offered to every ghost, with
// nothing to declare.
func TestLeaveNoteIsOffered(t *testing.T) {
	g, b, srv := modelGhost(t, 0, silent("hello"))
	g.markPrompted()
	b.Deliver(proto.Event{Type: proto.EventMessage, Text: "hello"})
	waitTurns(t, b, 1)
	for _, tool := range requests(t, srv)[0].Tools {
		if tool.Function.Name == "leave_note" {
			return
		}
	}
	t.Error("leave_note was not among the tools sent")
}

// The model leaves a note: it is an intention with a Note in the store, it
// is up, and the journal says so.
func TestLeaveNoteStored(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"text": "water the plants"})
	g, b, _ := modelGhost(t, 0,
		fake.Rule{Match: "note", AfterTool: "Left", Calls: []fake.Call{{Name: silentName}}},
		fake.Rule{Match: "note", Calls: []fake.Call{{Name: "leave_note", Args: args}}})
	g.markPrompted()
	b.Deliver(proto.Event{Type: proto.EventMessage, Text: "leave me a note to water the plants"})
	waitTurns(t, b, 1)

	notes := notesIn(g.loadIntentions())
	if len(notes) != 1 {
		t.Fatalf("%d notes stored, want 1: %+v", len(notes), g.loadIntentions())
	}
	if n := notes[0]; n.Text != "water the plants" || n.Name != "model/note-1" || n.Goal() {
		t.Errorf("stored %+v, want model/note-1 saying \"water the plants\", not a goal", n)
	}
	if !notes[0].Asked {
		t.Error("a note left while the person was talking is not marked as asked for")
	}
	if g.notesUp() != 1 {
		t.Errorf("%d notes up, want 1", g.notesUp())
	}
	journal := g.loadNotes()
	if len(journal) == 0 || journal[len(journal)-1].Text != "left a note: water the plants" {
		t.Errorf("journal = %+v, want it to end with the note", journal)
	}
}

// Three up at once; the fourth is refused with the three named, so the
// model can resolve one in its next call without asking what there is.
func TestLeaveNoteCap(t *testing.T) {
	g, _, _ := intendingGhost(t)
	for i := range maxDeskNotes {
		if _, err := leaveNote(g, fmt.Sprintf("note %d", i+1), ""); err != nil {
			t.Fatal(err)
		}
	}
	_, err := leaveNote(g, "one too many", "")
	if err == nil {
		t.Fatal("a fourth note was left")
	}
	for i := 1; i <= maxDeskNotes; i++ {
		if want := fmt.Sprintf("model/note-%d (\"note %d\")", i, i); !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not list %s: %v", want, err)
		}
	}
	if n := len(notesIn(g.loadIntentions())); n != maxDeskNotes || g.notesUp() != maxDeskNotes {
		t.Errorf("%d stored and %d up, want %d of each", n, g.notesUp(), maxDeskNotes)
	}
	// Taking one down makes room, and the next is numbered past the rest.
	g.execResolve("model/note-2")
	name, err := leaveNote(g, "room now", "")
	if err != nil || name != "model/note-4" {
		t.Errorf("after resolving one: %q, %v; want model/note-4", name, err)
	}
}

// A picture is pinned by its gallery id; one the gallery does not have is
// refused rather than left as a note with a hole in it.
func TestLeaveNoteUnknownPicture(t *testing.T) {
	g, _, _ := intendingGhost(t)
	_, err := toolLeaveNote(g, context.Background(), json.RawMessage(`{"text":"look","picture":"p9"}`))
	if err == nil || !strings.Contains(err.Error(), "no picture p9") {
		t.Errorf("an unknown picture: %v, want it refused", err)
	}
	if n := len(g.loadIntentions()); n != 0 {
		t.Errorf("%d intentions after a refusal, want none", n)
	}
	if err := g.putPicture(Picture{ID: "p2", Prompt: "a whale"}); err != nil {
		t.Fatal(err)
	}
	if _, err := toolLeaveNote(g, context.Background(), json.RawMessage(`{"text":"look","picture":"p2"}`)); err != nil {
		t.Fatalf("a known picture: %v", err)
	}
	if n := notesIn(g.loadIntentions()); len(n) != 1 || n[0].Note.Picture != "p2" {
		t.Errorf("stored %+v, want one note with p2 pinned", n)
	}
	// And the words are checked as the schema checks them.
	if _, err := toolLeaveNote(g, context.Background(), json.RawMessage(`{"text":"  "}`)); err == nil {
		t.Error("an empty note was left")
	}
	long, _ := json.Marshal(map[string]string{"text": strings.Repeat("x", maxNoteChars+1)})
	if _, err := toolLeaveNote(g, context.Background(), long); err == nil {
		t.Error("a note past the cap was left")
	}
}

// resolve takes the paper down with the record, and so does set_aside,
// and so does a click on it, which tells the model nothing.
func TestDeskNoteResolveTakesItDown(t *testing.T) {
	g, bus, _ := intendingGhost(t)
	a, _ := leaveNote(g, "first", "")
	b, _ := leaveNote(g, "second", "")
	if g.notesUp() != 2 {
		t.Fatalf("%d notes up, want 2", g.notesUp())
	}
	g.execResolve(a)
	if g.notesUp() != 1 {
		t.Errorf("after resolve, %d notes up, want 1", g.notesUp())
	}
	if n := notesIn(g.loadIntentions()); len(n) != 1 || n[0].Name != b {
		t.Errorf("after resolve the list holds %+v, want only %s", n, b)
	}
	g.execSetAside(b)
	if g.notesUp() != 0 {
		t.Errorf("after set_aside, %d notes up, want 0", g.notesUp())
	}
	if ctx := g.notesContext(); ctx != "" {
		t.Errorf("a note set aside is still in the context: %q", ctx)
	}
	select {
	case e := <-bus.Events():
		t.Errorf("taking notes down published %s", e.Type)
	default:
	}
}

// The turn context lists the notes up, after the goals, and does not count
// a note as a goal.
func TestDeskNoteInContext(t *testing.T) {
	g, _, _ := intendingGhost(t)
	if ctx := g.notesContext(); ctx != "" {
		t.Errorf("with no notes the context line is %q, want none", ctx)
	}
	intend(g, "model/thesis", "finish the thesis", 0)
	leaveNote(g, "water the plants", "")
	want := `Notes you have left: "water the plants" (model/note-1).`
	if got := g.notesContext(); got != "\n"+want {
		t.Errorf("notesContext() = %q, want %q", got, "\n"+want)
	}
	ctx := g.turnContext(time.Now())
	if !strings.Contains(ctx, want) {
		t.Errorf("the turn context does not list the note:\n%s", ctx)
	}
	goals, _, _ := strings.Cut(ctx[strings.Index(ctx, "Goals"):], "\n")
	if strings.Contains(goals, "water the plants") {
		t.Errorf("the note is listed as a goal: %s", goals)
	}
	if strings.Index(ctx, "Goals") > strings.Index(ctx, "Notes") {
		t.Errorf("the notes come before the goals:\n%s", ctx)
	}
}

// A note is in the store: a restart reads it back, with its picture and
// where it was dragged to, and puts it up again.
func TestDeskNoteSurvivesRestart(t *testing.T) {
	g, _, s := intendingGhost(t)
	g.putPicture(Picture{ID: "p1", Prompt: "a fern"})
	name, err := leaveNote(g, "the fern is thirsty", "p1")
	if err != nil {
		t.Fatal(err)
	}
	g.moveNote(name, "DP-1", 300, 120)

	raw, ok, _ := s.Get(IntentionsStoreKey)
	if !ok {
		t.Fatal("nothing stored")
	}
	list, err := DecodeIntentions(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Note == nil {
		t.Fatalf("decoded %+v, want one note", list)
	}
	want := DeskNote{Picture: "p1", Output: "DP-1", X: 300, Y: 120}
	if *list[0].Note != want || list[0].Text != "the fern is thirsty" {
		t.Errorf("decoded %+v %q, want %+v", *list[0].Note, list[0].Text, want)
	}
	// The intention's own keys are as they were; the note is beside them.
	if !strings.Contains(raw, `"n":"model/note-1"`) || !strings.Contains(raw, `"note":{`) {
		t.Errorf("stored as %s", raw)
	}

	next := &Ghost{store: s, log: hlog.Discard(), timers: map[string]*time.Timer{}}
	next.showStoredNotes()
	if next.notesUp() != 1 {
		t.Errorf("after a restart %d notes are up, want 1", next.notesUp())
	}
}

// Reminders filling the list push out the oldest reminders, never a note:
// the note is in front of the person, and the list is only a list.
func TestDeskNoteNeverEvictedByReminders(t *testing.T) {
	g, _, _ := intendingGhost(t)
	for i := range maxDeskNotes {
		leaveNote(g, fmt.Sprintf("note %d", i), "")
	}
	for i := range maxIntentions + 5 {
		intend(g, fmt.Sprintf("model/r%d", i), fmt.Sprintf("reminder %d", i), 60*60*1000)
	}
	list := g.loadIntentions()
	if len(list) != maxIntentions {
		t.Errorf("holding %d, want the cap %d", len(list), maxIntentions)
	}
	if n := len(notesIn(list)); n != maxDeskNotes {
		t.Errorf("%d notes left after the reminders, want all %d", n, maxDeskNotes)
	}
	if g.notesUp() != maxDeskNotes {
		t.Errorf("%d notes up, want %d", g.notesUp(), maxDeskNotes)
	}
	// The newest reminders are the ones kept.
	last := list[len(list)-1]
	if last.Text != fmt.Sprintf("reminder %d", maxIntentions+4) {
		t.Errorf("the newest kept is %q", last.Text)
	}
}

// A new note goes above the ghost, and above any note already there,
// without leaving the top of the screen.
func TestDeskNoteStacks(t *testing.T) {
	if y := stackNote(100, 500, 80, 60, nil); y != 500-60-noteGap {
		t.Errorf("the first note is at %d, want just above the ghost", y)
	}
	first := image.Rect(100, 432, 180, 492)
	y := stackNote(100, 500, 80, 60, []image.Rectangle{first})
	if y+60 > first.Min.Y {
		t.Errorf("the second note at %d covers the first at %d", y, first.Min.Y)
	}
	if y := stackNote(100, 30, 80, 60, nil); y != 0 {
		t.Errorf("a ghost at the top puts its note at %d, want 0", y)
	}
}
