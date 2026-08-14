package ghost

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aaronjauregui/haunt/internal/ai"
	"github.com/aaronjauregui/haunt/internal/shell"
)

var builtinPoses = []string{"asleep", "happy", "neutral", "sad", "surprised"}

func TestLexiconPose(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want string
	}{
		{"thanks reads happy", "Thank you, that's lovely.", "happy"},
		{"apology reads sad", "I'm sorry, I couldn't find it.", "sad"},
		{"exclamation reads surprised", "Wow, really?!", "surprised"},
		{"bedtime reads asleep", "Goodnight then.", "asleep"},
		{"plain statement suggests nothing", "The file is in that directory.", ""},
		{"case is ignored", "GOODNIGHT", "asleep"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := lexiconPose(tc.text, builtinPoses); got != tc.want {
				t.Errorf("lexiconPose(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

// TestLexiconOnlyPicksDeclaredPoses: a shell that has no "sad" must never
// be asked to wear one, however sad the line.
func TestLexiconOnlyPicksDeclaredPoses(t *testing.T) {
	if got := lexiconPose("I'm sorry, that's a shame.", []string{"neutral", "happy"}); got != "" {
		t.Errorf("picked %q, which the shell does not have", got)
	}
	if got := lexiconPose("anything", nil); got != "" {
		t.Errorf("picked %q for a shell with no poses", got)
	}
}

// TestLexiconIsDeterministic: the same line must always produce the same
// face, or a ghost re-reading its own dialogue would twitch between runs.
func TestLexiconIsDeterministic(t *testing.T) {
	const line = "Sorry! Wow, goodnight." // deliberately hits several moods
	first := lexiconPose(line, builtinPoses)
	for i := 0; i < 50; i++ {
		if got := lexiconPose(line, builtinPoses); got != first {
			t.Fatalf("run %d gave %q, first gave %q", i, got, first)
		}
	}
}

// classifierGhost is a ghost whose LLM answers with canned text.
func classifierGhost(t *testing.T, reply, mode string) (*Ghost, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		body, _ := json.Marshal(map[string]any{"choices": []map[string]any{
			{"delta": map[string]string{"content": reply}},
		}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", body)
	}))
	t.Cleanup(srv.Close)

	g := &Ghost{
		opts:     Options{Log: func(string) {}},
		manifest: &Manifest{Ghost: GhostSection{Capabilities: []string{"llm"}}},
		shell:    shell.Builtin(64),
		aiSvc: ai.NewService(&ai.Config{
			LLM:                 ai.LLMConfig{BaseURL: srv.URL + "/v1", Model: "fake"},
			ExpressionInference: mode,
		}, func(string) {}),
	}
	return g, &calls
}

// TestClassifyExpression: the model's answer is matched against the shell's
// own vocabulary, tolerating the padding small models add.
func TestClassifyExpression(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply string
		want  string
	}{
		{"bare word", "happy", "happy"},
		{"padded", "  The mood is: \"sad\".  ", "sad"},
		{"explained", "I would say surprised, because of the exclamation.", "surprised"},
		{"not in the vocabulary", "ecstatic", ""},
		{"empty-ish", "...", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, _ := classifierGhost(t, tc.reply, ai.ExpressionLLM)
			if got := g.classifyExpression("a line", g.shell.Poses()); got != tc.want {
				t.Errorf("classify(%q) = %q, want %q", tc.reply, got, tc.want)
			}
		})
	}
}

// TestExpressionModes: in llm mode the model decides for generated lines;
// the lexicon covers everything else and is the fallback.
func TestExpressionModes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mode      string
		line      string
		wantPose  string
		wantCalls int
	}{{
		name: "lexicon reads what it can", mode: ai.ExpressionLexicon,
		line: "I'm sorry about that.", wantPose: "sad", wantCalls: 0,
	}, {
		name: "lexicon shrugs and nobody is asked", mode: ai.ExpressionLexicon,
		line: "The build is done.", wantPose: "neutral", wantCalls: 0,
	}, {
		name: "the model overrules the word table", mode: ai.ExpressionLLM,
		line: "I'm sorry about that.", wantPose: "happy", wantCalls: 1,
	}, {
		name: "the model reads what the lexicon cannot", mode: ai.ExpressionLLM,
		line: "The build is done.", wantPose: "happy", wantCalls: 1,
	}, {
		name: "off is off", mode: ai.ExpressionOff,
		line: "I'm sorry about that.", wantPose: "neutral", wantCalls: 0,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			g, calls := classifierGhost(t, "happy", tc.mode)
			g.inferExpression(tc.line, true)
			if got := g.shell.Pose(); got != tc.wantPose {
				t.Errorf("pose = %q, want %q", got, tc.wantPose)
			}
			if *calls != tc.wantCalls {
				t.Errorf("made %d model calls, want %d", *calls, tc.wantCalls)
			}
		})
	}
}

// TestModelDeclining: a model that says "none" hands back to the lexicon
// rather than being pushed into a guess.
func TestModelDeclining(t *testing.T) {
	g, _ := classifierGhost(t, "none", ai.ExpressionLLM)
	g.inferExpression("The build is done.", true)
	if got := g.shell.Pose(); got != "neutral" {
		t.Errorf("pose = %q, want the face left alone", got)
	}

	// Declining does not throw away what the lexicon could read.
	g2, _ := classifierGhost(t, "none", ai.ExpressionLLM)
	g2.inferExpression("Goodnight.", true)
	if got := g2.shell.Pose(); got != "asleep" {
		t.Errorf("pose = %q, want the lexicon's asleep", got)
	}
}

// TestScriptedDialogueNeverCallsTheModel: the model tier is for generated
// lines. A scripted ghost's quips must stay free even with it enabled.
func TestScriptedDialogueNeverCallsTheModel(t *testing.T) {
	g, calls := classifierGhost(t, "happy", ai.ExpressionLLM)
	g.inferExpression("Goodnight.", false)
	if *calls != 0 {
		t.Errorf("a scripted line cost %d model calls", *calls)
	}
	if got := g.shell.Pose(); got != "asleep" {
		t.Errorf("pose = %q, want the lexicon's asleep", got)
	}
}

// TestInferenceLeavesSingleImageShellsAlone: nothing to arrange, nothing to
// spend.
func TestInferenceLeavesSingleImageShellsAlone(t *testing.T) {
	g, calls := classifierGhost(t, "happy", ai.ExpressionLLM)
	g.shell = nil
	g.inferExpression("Thanks!", true) // must not panic
	if *calls != 0 {
		t.Errorf("a ghost with no shell cost %d model calls", *calls)
	}
}
