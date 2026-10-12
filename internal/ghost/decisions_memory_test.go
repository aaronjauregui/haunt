package ghost

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/memory"
	"github.com/aaronjauregui/haunt/internal/model/fake"
	"github.com/aaronjauregui/haunt/internal/proto"
)

// seedMemory keeps lines in a model ghost's memory as from the sources
// given, dated a fortnight ago.
func seedMemory(t *testing.T, b *modelBrain, lines ...[3]string) {
	t.Helper()
	if b.mem == nil {
		t.Fatal("the ghost has no memory")
	}
	for _, l := range lines {
		p := memory.Provenance{Source: l[2], Turn: 1, At: time.Now().AddDate(0, 0, -14)}
		if err := b.mem.Remember(l[0], l[1], p); err != nil {
			t.Fatal(err)
		}
	}
}

// facts is a topic's live lines, as text.
func facts(t *testing.T, b *modelBrain, topic string) []string {
	t.Helper()
	fs, err := b.mem.Facts(topic)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range fs {
		out = append(out, f.Line)
	}
	return out
}

// A line the person stated is taken back only on their own turn: an event
// turn's forget is refused in words and the line stays, while a line from
// a page goes on any turn.
func TestForgetSourceRule(t *testing.T) {
	forgetArgs := func(line string) json.RawMessage {
		a, _ := json.Marshal(map[string]string{"topic": "where", "line": line})
		return a
	}
	g, b, srv := modelGhost(t, 3,
		fake.Rule{Match: "kind=idle", AfterTool: "*", Calls: []fake.Call{{Name: silentName}}},
		fake.Rule{Match: "kind=idle", Calls: []fake.Call{
			{Name: "forget", Args: forgetArgs("lives in Sydney")},
			{Name: "forget", Args: forgetArgs("the harbour bridge is closed")},
		}},
		fake.Rule{Match: "not Sydney", AfterTool: "*", Calls: []fake.Call{{Name: silentName}}},
		fake.Rule{Match: "not Sydney", Calls: []fake.Call{{Name: "forget", Args: forgetArgs("lives in sydney")}}})
	seedMemory(t, b,
		[3]string{"where", "lives in Sydney", "person"},
		[3]string{"where", "the harbour bridge is closed", "web"})

	b.Deliver(proto.Event{Type: proto.EventIdle})
	waitTurns(t, b, 1)
	if got := facts(t, b, "where"); len(got) != 1 || got[0] != "lives in Sydney" {
		t.Errorf("after the event turn the topic has %q, want the person's line alone", got)
	}
	var said strings.Builder
	for _, body := range srv.Bodies() {
		said.Write(body)
	}
	if !strings.Contains(said.String(), theirs) {
		t.Errorf("the refusal never reached the model")
	}

	g.markPrompted()
	b.Deliver(proto.Event{Type: proto.EventMessage, Text: "I'm not Sydney any more, forget that"})
	waitTurns(t, b, 2)
	if got := facts(t, b, "where"); len(got) != 0 {
		t.Errorf("after the person's turn the topic has %q", got)
	}
}

// The city case: the new line is kept, the old one moves to Past, and the
// result names it. On a turn that read a page, a person's line is not
// replaced and nothing is written.
func TestRememberReplaces(t *testing.T) {
	g, b, _ := modelGhost(t, 0)
	seedMemory(t, b, [3]string{"where", "lives in Sydney", "person"})

	g.startTurn(false, "idle")
	res := call(t, g, "remember", `{"topic":"where","line":"lives in Brisbane","replaces":"lives in Sydney"}`)
	if !res.IsError || !strings.Contains(res.Text, theirs) {
		t.Errorf("an event turn replacing the person's line answered %q", res.Text)
	}
	if got := facts(t, b, "where"); len(got) != 1 || got[0] != "lives in Sydney" {
		t.Errorf("after the refusal the topic has %q", got)
	}

	g.startTurn(true, "")
	res = call(t, g, "remember", `{"topic":"where","line":"lives in Brisbane","replaces":"Lives in  Sydney"}`)
	if res.IsError || res.Text != "Kept. Retired: lives in Sydney." {
		t.Errorf("remember answered %q", res.Text)
	}
	tp, err := memory.ReadTopic(b.memDir, "where")
	if err != nil {
		t.Fatal(err)
	}
	if len(tp.Facts) != 1 || tp.Facts[0].Line != "lives in Brisbane" {
		t.Errorf("facts = %+v", tp.Facts)
	}
	if len(tp.Past) != 1 || tp.Past[0].Line != "lives in Sydney" || tp.Past[0].Retired.IsZero() {
		t.Errorf("past = %+v", tp.Past)
	}
	raw, _ := os.ReadFile(filepath.Join(b.memDir, "topics", "where.md"))
	if !strings.Contains(string(raw), "## Past\n- lives in Sydney <!-- person t=1 ") ||
		!strings.Contains(string(raw), "<!-- retired "+time.Now().Format("2006-01-02")+" -->") {
		t.Errorf("the file is\n%s", raw)
	}

	// Naming a line the topic does not have keeps the new one and says
	// what is there.
	res = call(t, g, "remember", `{"topic":"where","line":"works in the city","replaces":"works from home"}`)
	if res.IsError || res.Text != "Kept. No line 'works from home' to retire; the topic has: 'lives in Brisbane'" {
		t.Errorf("remember naming nothing answered %q", res.Text)
	}
}

// Without replaces, the result lists the topic's other lines, at most a
// dozen, so the model can see one the new line makes false.
func TestRememberListsOthers(t *testing.T) {
	g, b, _ := modelGhost(t, 0)
	g.startTurn(true, "")
	if res := call(t, g, "remember", `{"topic":"where","line":"lives in Sydney"}`); res.Text != "Remembered under where, as from person." {
		t.Errorf("the first line answered %q", res.Text)
	}
	res := call(t, g, "remember", `{"topic":"where","line":"lives in Brisbane"}`)
	if !strings.Contains(res.Text, "The topic also has: 'lives in Sydney'.") || !strings.Contains(res.Text, "replaces") {
		t.Errorf("the second line answered %q", res.Text)
	}
	for i := 0; i < 14; i++ {
		seedMemory(t, b, [3]string{"where", "visited town " + string(rune('a'+i)), "person"})
	}
	res = call(t, g, "remember", `{"topic":"where","line":"likes the beach"}`)
	if n := strings.Count(res.Text, "'"); n != 24 || !strings.Contains(res.Text, "and 4 more") {
		t.Errorf("a long topic answered %q", res.Text)
	}
}
