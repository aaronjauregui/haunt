package library

import (
	"os"
	"testing"
	"time"
)

// A permission you can only withdraw by restarting the thing is not really
// withdrawn: the watcher is what lets a running ghost start refusing.
func TestWatchSeesARevocation(t *testing.T) {
	t.Setenv("HAUNT_DATA_DIR", t.TempDir())
	if err := SaveGrants("wisp", &Grants{Capabilities: []string{"message", "llm", "window"}}); err != nil {
		t.Fatal(err)
	}

	w, err := Watch("wisp")
	if err != nil {
		t.Fatal(err)
	}
	var granted, revoked []string
	w.OnChange = func(g, r []string) { granted, revoked = g, r }

	if !w.Granted("llm") || !w.Granted("window") {
		t.Fatal("the watcher did not see the grants it started with")
	}

	// The user changes their mind about window titles.
	if err := SaveGrants("wisp", &Grants{Capabilities: []string{"message", "llm"}}); err != nil {
		t.Fatal(err)
	}
	// Past the poll interval, since the answer is cached for a moment: this
	// is checked several times per exchange and must not stat every time.
	waitFor(t, func() bool { return !w.Granted("window") })

	if w.Granted("window") {
		t.Error("the revoked capability is still granted")
	}
	if !w.Granted("llm") {
		t.Error("revoking one capability took another with it")
	}
	if len(revoked) != 1 || revoked[0] != "window" {
		t.Errorf("OnChange reported revoked = %v", revoked)
	}
	if len(granted) != 0 {
		t.Errorf("OnChange reported gains that did not happen: %v", granted)
	}
}

// The other direction, for the same reason: a grant should not need a
// restart either.
func TestWatchSeesAGrant(t *testing.T) {
	t.Setenv("HAUNT_DATA_DIR", t.TempDir())
	if err := SaveGrants("wisp", &Grants{Capabilities: []string{"message"}}); err != nil {
		t.Fatal(err)
	}
	w, err := Watch("wisp")
	if err != nil {
		t.Fatal(err)
	}
	var granted []string
	w.OnChange = func(g, _ []string) { granted = g }

	if w.Granted("llm") {
		t.Fatal("llm was granted before anyone said so")
	}
	if err := SaveGrants("wisp", &Grants{Capabilities: []string{"message", "llm"}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return w.Granted("llm") })
	if len(granted) != 1 || granted[0] != "llm" {
		t.Errorf("OnChange reported granted = %v", granted)
	}
}

// A ghost installed with nothing granted has no grants file at all, which
// must read as "nothing granted" rather than as an error.
func TestWatchWithNoGrantsFile(t *testing.T) {
	t.Setenv("HAUNT_DATA_DIR", t.TempDir())
	w, err := Watch("wisp")
	if err != nil {
		t.Fatal(err)
	}
	if w.Granted("llm") {
		t.Error("a ghost with no grants file was granted something")
	}
	if got := w.Grants(); len(got) != 0 {
		t.Errorf("grants = %v", got)
	}

	// And it notices when one appears.
	if err := SaveGrants("wisp", &Grants{Capabilities: []string{"llm"}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return w.Granted("llm") })
}

// A file caught mid-write must not widen or narrow what the ghost may do.
func TestWatchIgnoresAnUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HAUNT_DATA_DIR", dir)
	if err := SaveGrants("wisp", &Grants{Capabilities: []string{"llm"}}); err != nil {
		t.Fatal(err)
	}
	w, err := Watch("wisp")
	if err != nil {
		t.Fatal(err)
	}
	if !w.Granted("llm") {
		t.Fatal("setup")
	}

	ghostDir, err := GhostDir("wisp")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ghostDir+"/grants.toml", []byte("capabilities = [\"ll"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * grantPollInterval)
	if !w.Granted("llm") {
		t.Error("half a file changed what the ghost may do")
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("the watcher never noticed the change")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
