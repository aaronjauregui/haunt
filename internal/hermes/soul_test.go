package hermes

import (
	"strings"
	"testing"
)

// A ghost with poses is told how to open a reply with one, and which it
// has; one without is told it has a single face.
func TestSoulPoses(t *testing.T) {
	s := Soul(Persona{Name: "wisp", Poses: []string{"neutral", "happy"}})
	for _, want := range []string{"open your reply with one of your poses in square brackets", "`[neutral] ...`",
		"Your poses are: neutral, happy.", "Only a pose name goes in them", "The `face` tool changes it without words."} {
		if !strings.Contains(s, want) {
			t.Errorf("SOUL.md lacks %q:\n%s", want, s)
		}
	}
	if s := Soul(Persona{Name: "wisp"}); !strings.Contains(s, "single face") || strings.Contains(s, "square brackets") {
		t.Errorf("a one-face ghost is told about poses:\n%s", s)
	}
}
