package hermes

import "testing"

// A leading tag is taken off whatever it names; one anywhere else is words.
func TestCutPose(t *testing.T) {
	for _, c := range []struct{ in, pose, rest string }{
		{"[happy] Oh, it's you.", "happy", "Oh, it's you."},
		{"  [sad]\nGone already?", "sad", "Gone already?"},
		{"[surprised_2]Oh!", "surprised_2", "Oh!"},
		{"[sighs] Fine.", "sighs", "Fine."},
		{"Oh [happy] hello", "", "Oh [happy] hello"},
		{"[not a pose] hi", "", "[not a pose] hi"},
		{"[happy] NO_REPLY", "happy", "NO_REPLY"},
		{"plain", "", "plain"},
	} {
		pose, rest := CutPose(c.in)
		if pose != c.pose || rest != c.rest {
			t.Errorf("CutPose(%q) = %q, %q; want %q, %q", c.in, pose, rest, c.pose, c.rest)
		}
	}
}

// A stream is held while it could still be opening a tag, and no longer.
func TestMaybePose(t *testing.T) {
	for in, want := range map[string]bool{
		"": true, " ": true, "[": true, "[hap": true, " [happy": true,
		"[happy]": false, "[happy] Oh": false, "Oh": false, "[not a": false,
		"[" + string(make([]byte, 40)): false,
	} {
		if got := MaybePose(in); got != want {
			t.Errorf("MaybePose(%q) = %v, want %v", in, got, want)
		}
	}
}
