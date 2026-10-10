package ghost

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// A model's guessed argument names land on the decision's own, unless the
// right one is there too; ask without its question is a failure to retry.
func TestArgAliases(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"skill_view", `{"skill":"humanizer"}`, `{"name":"humanizer"}`},
		{"skill_write", `{"skill":"x","body":"b"}`, `{"body":"b","name":"x"}`},
		{"recall", `{"topic":"coffee"}`, `{"query":"coffee"}`},
		{"ask", `{"question":"q","choices":["a"]}`, `{"choices":["a"],"text":"q"}`},
		{"ask", `{"question":"q","text":"t"}`, `{"question":"q","text":"t"}`},
		{"remember", `{"topic":"food","line":"x"}`, `{"topic":"food","line":"x"}`},
		{"recall", `[1]`, `[1]`},
	} {
		if got := string(unalias(c.name, json.RawMessage(c.in))); got != c.want {
			t.Errorf("%s %s = %s, want %s", c.name, c.in, got, c.want)
		}
	}

	g, _, _ := modelGhost(t, 0)
	g.surface = nil
	r := g.call(context.Background(), "ask", json.RawMessage(`{"choices":["a","b"]}`))
	if !r.IsError || !strings.Contains(r.Text, "ask needs text") {
		t.Errorf("ask with no question answered %+v", r)
	}
	// The aliased question gets past that, to the next refusal.
	r = g.call(context.Background(), "ask", json.RawMessage(`{"question":"Which?","choices":["a","b"]}`))
	if !r.IsError || !strings.Contains(r.Text, "no way to ask") {
		t.Errorf("ask with question answered %+v", r)
	}
}

// textCalls reads the calls Gemma 4 wrote out as text in the evals, with
// arrays, words before them and the arguments in its own notation, and
// leaves anything else to be words.
func TestTextCalls(t *testing.T) {
	ds := (&Ghost{}).offered(false)
	type call struct{ name, args string }
	for _, c := range []struct {
		text  string
		calls []call
		rest  string
		found bool
		bad   bool
	}{
		{
			text:  `<|tool_call>call:skill_view{skill:<|"|>humanizer<|"|>}<tool_call|>`,
			calls: []call{{"skill_view", `{"skill":"humanizer"}`}},
			found: true,
		},
		{
			text: `<|tool_call>call:ask{choices:[<|"|>I'll tell you<|"|>,<|"|>Not today<|"|>],` +
				`question:<|"|>What's the one thing you want done today?<|"|>}<tool_call|>`,
			calls: []call{{"ask", `{"choices":["I'll tell you","Not today"],"question":"What's the one thing you want done today?"}`}},
			found: true,
		},
		{
			text:  `Morning. Try not to stare at me.<tool_call|><|tool_call>call:ask{text:<|"|>Plans?<|"|>,choices:[<|"|>Yes<|"|>]}<tool_call|>`,
			calls: []call{{"ask", `{"text":"Plans?","choices":["Yes"]}`}},
			rest:  "Morning. Try not to stare at me.",
			found: true,
		},
		{
			text:  `ask{choices:["a", "b"], text:"Pick <one>", n: 2, ok: true}`,
			calls: []call{{"ask", `{"choices":["a", "b"],"text":"Pick <one>","n": 2,"ok": true}`}},
			found: true,
		},
		{
			text:  `call:intentions{} recall{query:coffee}`,
			calls: []call{{"intentions", `{}`}, {"recall", `{"query":"coffee"}`}},
			found: true,
		},
		{text: "Hello there."},
		{text: "I {love} it."},
		{text: "nope{a:1}"},
		{text: `<|tool_call>call:nope{a:1}<tool_call|>`, found: true, bad: true},
		{text: `<|tool_call>call:ask{choices:[<|"|>Yes`, found: true, bad: true},
		{text: `<|tool_call>call:ask{text one two}`, found: true, bad: true},
		{text: `ask{text:<|"|>a<|"|>} and then some words`, found: true, bad: true},
	} {
		calls, rest, found, err := textCalls(c.text, ds)
		if found != c.found || (err != nil) != c.bad {
			t.Errorf("%q: found %v, err %v", c.text, found, err)
			continue
		}
		if c.bad || !found {
			continue
		}
		if rest != c.rest || len(calls) != len(c.calls) {
			t.Errorf("%q: %d calls, rest %q", c.text, len(calls), rest)
			continue
		}
		for i, want := range c.calls {
			// Spaces in the source survive; compare without them.
			got := strings.ReplaceAll(string(calls[i].Args), " ", "")
			if calls[i].Name != want.name || got != strings.ReplaceAll(want.args, " ", "") {
				t.Errorf("%q: call %d = %s %s, want %s %s", c.text, i, calls[i].Name, calls[i].Args, want.name, want.args)
			}
		}
	}
}

// The balloon stops at Gemma's call tokens however the stream splits
// them, and is given a held tail that turned out not to be one.
func TestHeldCalls(t *testing.T) {
	for _, c := range []struct{ text, shown string }{
		{`On it.<tool_call|><|tool_call>call:ask{}`, "On it."},
		{`<|tool_call>call:skill_view{name:x}`, ""},
		{`Plain words <| and more`, `Plain words <| and more`},
		{`Ends on <|tool`, `Ends on <|tool`},
	} {
		for _, size := range []int{1, 3, 100} {
			var b strings.Builder
			h := &heldCalls{out: func(s string) { b.WriteString(s) }}
			for i := 0; i < len(c.text); i += size {
				h.delta(c.text[i:min(i+size, len(c.text))])
			}
			h.flush()
			if b.String() != c.shown {
				t.Errorf("%q in pieces of %d: shown %q, want %q", c.text, size, b.String(), c.shown)
			}
		}
	}
}
