package ghost

import (
	"testing"

	"github.com/aaronjauregui/haunt/internal/config"
	"github.com/aaronjauregui/haunt/internal/desktop"
	"github.com/aaronjauregui/haunt/internal/hlog"
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

// expressionGhost is a ghost with the built-in shell and the given
// expression mode.
func expressionGhost(mode string) *Ghost {
	return &Ghost{
		opts:           Options{},
		log:            hlog.Discard(),
		manifest:       &Manifest{Ghost: GhostSection{Capabilities: []string{"llm"}}},
		shell:          shell.Builtin(64),
		expressionMode: mode,
	}
}

// TestExpressionModes: the lexicon reads what it can, and off is off. The
// "llm" mode went with the one-shot loop; a config that still says it gets
// the lexicon.
func TestExpressionModes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     string
		line     string
		wantPose string
	}{
		{"lexicon reads what it can", config.ExpressionLexicon, "I'm sorry about that.", "sad"},
		{"lexicon shrugs", config.ExpressionLexicon, "The build is done.", "neutral"},
		{"off is off", config.ExpressionOff, "I'm sorry about that.", "neutral"},
		{"the old llm mode reads as the lexicon", "llm", "Goodnight.", "asleep"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := expressionGhost(tc.mode)
			g.inferExpression(tc.line)
			if got := g.shell.Pose(); got != tc.wantPose {
				t.Errorf("pose = %q, want %q", got, tc.wantPose)
			}
		})
	}
}

// TestInferenceLeavesSingleImageShellsAlone: nothing to arrange.
func TestInferenceLeavesSingleImageShellsAlone(t *testing.T) {
	g := expressionGhost(config.ExpressionLexicon)
	g.shell = nil
	g.inferExpression("Thanks!") // must not panic
}

// The shell format advertises which pose names the driver can choose on its
// own (shell.MoodPoses); this table is what actually does the choosing. If
// they drift, a shell author — or the SERIKO converter — is told to use a
// name that no longer does anything, which is worse than saying nothing.
func TestMoodTableMatchesTheAdvertisedPoses(t *testing.T) {
	advertised := map[string]bool{}
	for _, p := range shell.MoodPoses {
		advertised[p] = true
	}
	for pose := range moodWords {
		if !advertised[pose] {
			t.Errorf("the mood table has %q, which shell.MoodPoses does not advertise", pose)
		}
	}
	for _, pose := range shell.MoodPoses {
		if _, ok := moodWords[pose]; !ok {
			t.Errorf("shell.MoodPoses advertises %q, which the mood table cannot infer", pose)
		}
	}
}

// The gap a converted ukagaka shell falls into. SERIKO identifies surfaces
// by number, so an imported shell's poses are "s0", "s1", … — and inference
// chooses by *name*. Nothing matches, nothing is logged, and the ghost
// wears one face forever while looking like a bug in the driver.
func TestNumberedPosesCannotBeInferred(t *testing.T) {
	const line = "I am so happy today, this is wonderful"

	numbered := []string{"s0", "s1", "s2", "s3"}
	if got := lexiconPose(line, numbered); got != "" {
		t.Errorf("lexiconPose picked %q from numbered poses; it should find nothing", got)
	}

	// Naming even one of them is enough for the face to follow the words,
	// which is what `haunt-seriko -pose happy=3` is for.
	named := append([]string{}, numbered...)
	named = append(named, "happy")
	if got := lexiconPose(line, named); got != "happy" {
		t.Errorf("lexiconPose = %q with a named pose available, want happy", got)
	}
}

// TestMayPoseInheritsTheIdleRules: the offer is the driver's half of the
// face at rest, and withholding it is how the two rules a face change
// inherits are enforced. A pose over a fullscreen window is the intrusion
// fullscreen exists to exclude; a pose under reduced motion is something
// the person asked not to have.
func TestMayPoseInheritsTheIdleRules(t *testing.T) {
	g := &Ghost{log: hlog.Discard(), shell: shell.Builtin(64)}
	if !g.mayPose() {
		t.Error("an ordinary moment was not offered")
	}

	// Nothing to ask about fullscreen windows answers "not fullscreen":
	// the failure mode is a face that changes when it might not have, not
	// a ghost frozen because a compositor socket moved.
	g.desktop = desktop.New(hlog.Discard())
	if !g.mayPose() {
		t.Error("no compositor IPC turned the face off entirely")
	}

	g.reducedMotion = true
	if g.mayPose() {
		t.Error("offered a pose under reduced motion")
	}
}

// TestBootCarriesTheShellsPoses: a brain choosing a face needs to know
// which faces exist — the driver ignores the ones the shell does not have,
// so without the list a brain is guessing into silence.
func TestBootCarriesTheShellsPoses(t *testing.T) {
	sh := shell.Builtin(64)
	if got := sh.Poses(); len(got) == 0 {
		t.Fatal("the built-in shell declares no poses")
	}
	// The same list the driver checks against in setExpression, so the two
	// cannot drift into a brain being told about a pose it may not wear.
	for _, p := range sh.Poses() {
		if !sh.SetPose(p) {
			t.Errorf("pose %q is advertised but not wearable", p)
		}
	}
}
