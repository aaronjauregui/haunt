package ghost

import (
	"strings"
	"testing"

	"github.com/aaronjauregui/haunt/internal/ai"
	"github.com/aaronjauregui/haunt/internal/hlog"
)

// A tool's answer is the one thing in a prompt the driver does not
// generate and cannot predict the size of: an MCP server is somebody
// else's program, and a file or a query dump used to go in whole. Six of
// them may run in one exchange.
func TestALongToolAnswerIsCut(t *testing.T) {
	g := &Ghost{log: hlog.Discard()}
	ref := toolRef{Server: "notes", Tool: "search"}

	got := g.readable(ref, strings.Repeat("x", toolResultChars*3))
	if len(got) <= toolResultChars {
		t.Fatalf("result trimmed to %d characters, want the content plus a note", len(got))
	}
	body, note, found := strings.Cut(got, "\n\n[")
	if !found {
		t.Fatal("the answer was cut with nothing to say so")
	}
	if len(body) != toolResultChars {
		t.Errorf("kept %d characters, want %d", len(body), toolResultChars)
	}
	// The number named is what was actually dropped, not what was kept.
	if !strings.Contains(note, "4000 more characters") {
		t.Errorf("the note does not say how much was lost: %q", note)
	}
}

// Saying so is the point rather than a courtesy: a silently truncated
// answer is worse than a refused one, because the model reads half a
// result, cannot tell that it is half, and answers as though it had the
// whole thing.
func TestAShortToolAnswerIsUntouched(t *testing.T) {
	g := &Ghost{log: hlog.Discard()}
	ref := toolRef{Server: "notes", Tool: "search"}

	for _, text := range []string{"", "nothing found", strings.Repeat("y", toolResultChars)} {
		if got := g.readable(ref, text); got != text {
			t.Errorf("an answer of %d characters was altered", len(text))
		}
	}
}

// A tool may answer in any script, and half a character is not a saving.
func TestCuttingAToolAnswerKeepsCharactersWhole(t *testing.T) {
	g := &Ghost{log: hlog.Discard()}
	ref := toolRef{Server: "notes", Tool: "search"}

	// Three-byte runes, so the cut lands mid-character unless it is moved.
	got := g.readable(ref, strings.Repeat("あ", toolResultChars))
	body, _, _ := strings.Cut(got, "\n\n[")
	if !strings.HasPrefix(strings.Repeat("あ", toolResultChars), body) {
		t.Error("the cut did not land on a character boundary")
	}
	for _, r := range body {
		if r == '�' {
			t.Fatal("the cut produced a broken character")
		}
	}
}

// The structure survives the cut. A tool's answer is often a list, and
// flattening it to one line the way the journal's clip() does would cost
// the model the shape it needs to read it.
func TestCuttingAToolAnswerKeepsItsShape(t *testing.T) {
	g := &Ghost{log: hlog.Discard()}
	lines := strings.Repeat("- an appointment at some time\n", 200)
	got := g.readable(toolRef{Server: "cal", Tool: "today"}, lines)
	if strings.Count(got, "\n") < 50 {
		t.Errorf("the answer was flattened: %d newlines left", strings.Count(got, "\n"))
	}
}

// The measurement behind the reply trace: what was actually sent, counting
// the tool-call arguments that ride along with an assistant turn.
func TestPromptCharsCountsWhatIsSent(t *testing.T) {
	var call ai.ToolCall
	call.Function.Name, call.Function.Arguments = "ab", "cde"
	msgs := []ai.Message{
		{Role: "system", Content: "abcde"},
		{Role: "user", Content: "fghij"},
		{Role: "assistant", ToolCalls: []ai.ToolCall{call}},
		{Role: "tool", Content: "klm"},
	}
	if got, want := promptChars(msgs), 5+5+2+3+3; got != want {
		t.Errorf("promptChars = %d, want %d", got, want)
	}
	if got := promptChars(nil); got != 0 {
		t.Errorf("promptChars(nil) = %d, want 0", got)
	}
}
