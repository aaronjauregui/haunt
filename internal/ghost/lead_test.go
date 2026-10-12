package ghost

import (
	"strings"
	"testing"

	"github.com/aaronjauregui/haunt/internal/model/fake"
	"github.com/aaronjauregui/haunt/internal/proto"
	"github.com/aaronjauregui/haunt/internal/textinput"
)

// hintLine is the line part 6 ends with on a turn the person led.
const hintLine = `Begin your reply with: "Well, actually"`

// rowsOfKind is the live rows of one kind, oldest first.
func rowsOfKind(t *testing.T, b *modelBrain, kind string) []TranscriptRow {
	t.Helper()
	rows, err := b.tr.all()
	if err != nil {
		t.Fatal(err)
	}
	var out []TranscriptRow
	for _, r := range rows {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

// TestLeadIsTheSpeakPrefill: a lead on the person's message is the speak
// call's prefill in place of the persona's, the words open with it, and
// the decide call's context carries the hint. The turn after has the
// persona's prefill again and no hint: a lead is one-shot.
func TestLeadIsTheSpeakPrefill(t *testing.T) {
	g, b, srv := modelGhost(t, 0, append(
		speaks("hello", "happy", ", I was expecting you."),
		speaks("again", "happy", "you again.")...)...)
	withPoses(t, g, "neutral", "happy")
	g.surface = nil
	g.manifest.Persona.Prefill = "Oh, "
	g.markPrompted()

	b.Deliver(proto.Event{Type: proto.EventMessage, Text: "hello", Lead: "Well, actually"})
	waitTurns(t, b, 1)
	reqs := requests(t, srv)
	if len(reqs) != 2 {
		t.Fatalf("%d calls, want decide then speak", len(reqs))
	}
	ctx := reqs[0].Messages[len(reqs[0].Messages)-1]
	if ctx.Role != "system" || !strings.HasSuffix(ctx.Content, hintLine) {
		t.Errorf("the decide call's context ends %q, want the hint", ctx.Content)
	}
	sp := reqs[1].Messages
	if last := sp[len(sp)-1]; last.Role != "assistant" || last.Content != "Well, actually" {
		t.Errorf("the speak call ends %+v, want the lead as its prefill", last)
	}
	words := rowsOfKind(t, b, rowWords)
	if len(words) != 1 || words[0].Text != "Well, actually, I was expecting you." {
		t.Fatalf("words = %+v, want them opened by the lead", words)
	}
	leads := rowsOfKind(t, b, rowLead)
	if len(leads) != 1 || leads[0].Text != "Well, actually" || leads[0].Tool != leadPrefilled {
		t.Errorf("lead rows = %+v", leads)
	}

	// The next turn: the persona's prefill, no hint, and the lead row is
	// not replayed as anything.
	g.endResponding()
	b.Deliver(proto.Event{Type: proto.EventMessage, Text: "again"})
	waitTurns(t, b, 2)
	reqs = requests(t, srv)
	if len(reqs) != 4 {
		t.Fatalf("%d calls, want two more", len(reqs))
	}
	for _, m := range reqs[2].Messages {
		if strings.Contains(m.Content, "Begin your reply with") {
			t.Errorf("the next turn carries the hint in a %s message: %q", m.Role, m.Content)
		}
	}
	sp = reqs[3].Messages
	if last := sp[len(sp)-1]; last.Role != "assistant" || last.Content != "Oh, " {
		t.Errorf("the next speak call ends %+v, want the persona's prefill", last)
	}
	if leads := rowsOfKind(t, b, rowLead); len(leads) != 1 {
		t.Errorf("lead rows after the next turn = %+v, want the one", leads)
	}
}

// TestLeadHintWhenTheDecideCallWrites: when the words are the decide
// call's own text, nothing is prefilled; the context asks for the lead
// and the words are what the model wrote, recorded as a hint.
func TestLeadHintWhenTheDecideCallWrites(t *testing.T) {
	g, b, srv := modelGhost(t, 0,
		fake.Rule{Match: "hello", Reply: "Hello yourself."},
		fake.Rule{Match: "", Reply: "Mm."})
	g.surface = nil
	g.manifest.Persona.Prefill = "Oh, "
	g.markPrompted()

	b.Deliver(proto.Event{Type: proto.EventMessage, Text: "hello", Lead: "Well, actually"})
	waitTurns(t, b, 1)
	reqs := requests(t, srv)
	if len(reqs) != 1 {
		t.Fatalf("%d calls, want the decide call alone", len(reqs))
	}
	msgs := reqs[0].Messages
	if last := msgs[len(msgs)-1]; last.Role != "system" || !strings.HasSuffix(last.Content, hintLine) {
		t.Errorf("the decide call ends %+v, want the context with the hint", last)
	}
	if words := rowsOfKind(t, b, rowWords); len(words) != 1 || words[0].Text != "Hello yourself." {
		t.Errorf("words = %+v, want them as the model wrote them", words)
	}
	leads := rowsOfKind(t, b, rowLead)
	if len(leads) != 1 || leads[0].Tool != leadHinted {
		t.Errorf("lead rows = %+v, want one given as a hint", leads)
	}

	g.endResponding()
	b.Deliver(proto.Event{Type: proto.EventMessage, Text: "and now"})
	waitTurns(t, b, 2)
	reqs = requests(t, srv)
	if strings.Contains(string(srv.Bodies()[len(reqs)-1]), "Begin your reply with") {
		t.Error("the turn after the lead still carries the hint")
	}
}

// TestLeadNotSearchedOrSummarised: a lead row is no one's words to
// recall, and the summary call is never told the ghost was led.
func TestLeadNotSearchedOrSummarised(t *testing.T) {
	if searchable(rowLead) {
		t.Error("recall searches the lead")
	}
	if d := digestLine(TranscriptRow{Kind: rowLead, Text: "Well, actually"}); d != "" {
		t.Errorf("digest of a lead = %q", d)
	}
	msgs := replay([]TranscriptRow{
		{Turn: 1, Kind: rowInput, Text: "hello"},
		{Turn: 1, Kind: rowLead, Text: "Well, actually", Tool: leadPrefilled},
		{Turn: 1, Kind: rowWords, Text: "Well, actually, hello."},
	})
	if len(msgs) != 2 || msgs[0].Content != "hello" || msgs[1].Content != "Well, actually, hello." {
		t.Errorf("replay = %+v, want the input and the words only", msgs)
	}
}

// typeLine types any US-layout text, shift included.
func typeLine(g *Ghost, s string) {
	const shiftKey = 42
	for _, r := range s {
		code, shift, ok := textinput.Scancode(r)
		if !ok {
			panic("typeLine: no key for " + string(r))
		}
		if shift {
			g.onKey(shiftKey, true)
		}
		g.onKey(code, true)
		g.onKey(code, false)
		if shift {
			g.onKey(shiftKey, false)
		}
	}
}

// TestLeadInTheLine: `/lead WORDS` in the line is not sent; it sets the
// lead, the line stays open and shows it, and the next line goes with it
// and takes it. Escape takes a lead back without ending the line, and an
// empty /lead does too.
func TestLeadInTheLine(t *testing.T) {
	g, b := typingGhost(t)
	g.openInput()
	typeLine(g, "/lead Well, actually")
	g.onKey(textinput.KeyEnter, true)
	if !g.inputOpen() {
		t.Fatal("the line closed on /lead")
	}
	if events := drain(b); len(events) != 0 {
		t.Fatalf("/lead published %+v", events)
	}
	if g.input.lead != "Well, actually" || g.input.editor.Text() != "" {
		t.Errorf("lead = %q, line = %q", g.input.lead, g.input.editor.Text())
	}

	typeLine(g, "what do you think?")
	g.onKey(textinput.KeyEnter, true)
	events := drain(b)
	if len(events) != 1 || events[0].Text != "what do you think?" || events[0].Lead != "Well, actually" {
		t.Fatalf("published %+v, want the line with the lead", events)
	}
	typeLine(g, "and then?")
	g.onKey(textinput.KeyEnter, true)
	if events := drain(b); len(events) != 1 || events[0].Lead != "" {
		t.Fatalf("the line after carried %+v, want no lead", events)
	}

	// Escape takes a lead back; the line stays.
	typeLine(g, "/lead Hm.")
	g.onKey(textinput.KeyEnter, true)
	g.onKey(textinput.KeyEsc, true)
	if !g.inputOpen() || g.input.lead != "" {
		t.Errorf("after escape: open %v, lead %q", g.inputOpen(), g.input.lead)
	}
	// An empty /lead does too.
	typeLine(g, "/lead Hm.")
	g.onKey(textinput.KeyEnter, true)
	typeLine(g, "/lead")
	g.onKey(textinput.KeyEnter, true)
	if g.input.lead != "" {
		t.Errorf("an empty /lead left %q", g.input.lead)
	}
	// A word that only starts like the command is a line.
	typeLine(g, "/leading")
	g.onKey(textinput.KeyEnter, true)
	if events := drain(b); len(events) != 1 || events[0].Text != "/leading" {
		t.Errorf("/leading published %+v", events)
	}
	// Closing the line drops a lead not yet sent.
	typeLine(g, "/lead Hm.")
	g.onKey(textinput.KeyEnter, true)
	g.onKey(textinput.KeyEsc, true)
	g.onKey(textinput.KeyEsc, true)
	if g.inputOpen() {
		t.Error("a second escape left the line open")
	}
	g.openInput()
	typeLine(g, "hi")
	g.onKey(textinput.KeyEnter, true)
	if events := drain(b); len(events) != 1 || events[0].Lead != "" {
		t.Errorf("a new line carried %+v", events)
	}
}

// TestLeadInTheLineAnswersAQuestion: a line answering the driver's own
// question goes to the question, /lead or not.
func TestLeadInTheLineAnswersAQuestion(t *testing.T) {
	g, _ := typingGhost(t)
	reply := make(chan string, 1)
	if !g.openInputWithReply(reply) {
		t.Fatal("the question's line did not open")
	}
	typeLine(g, "/lead no")
	g.onKey(textinput.KeyEnter, true)
	if got := <-reply; got != "/lead no" {
		t.Errorf("answer = %q", got)
	}
}

// TestLeadCommandIsTheWholeText: text of several lines that starts with
// /lead is a message, not a lead.
func TestLeadCommandIsTheWholeText(t *testing.T) {
	g, b := typingGhost(t)
	g.openInput()
	typeLine(g, "/lead Well,")
	g.onKey(textinput.KeyBackslash, true)
	g.onKey(textinput.KeyBackslash, false)
	g.onKey(textinput.KeyEnter, true)
	g.onKey(textinput.KeyEnter, false)
	typeLine(g, "what now?")
	g.onKey(textinput.KeyEnter, true)
	events := drain(b)
	if len(events) != 1 || events[0].Text != "/lead Well,\nwhat now?" || events[0].Lead != "" {
		t.Fatalf("published %+v, want the two lines as a message with no lead", events)
	}
	if g.input.lead != "" {
		t.Errorf("lead = %q", g.input.lead)
	}
}
