package control

import (
	"os"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/proto"
)

// listen brings a ghost up on its control socket for the length of a test.
func listen(t *testing.T, name string) {
	t.Helper()
	s, err := Listen(name, func(proto.Event) error { return nil }, hlog.Discard())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
}

// With one ghost running there is nothing to decide.
func TestRecentIsTheOnlyGhostRunning(t *testing.T) {
	t.Setenv("HAUNT_RUNTIME_DIR", t.TempDir())
	listen(t, "wisp")
	if got := Recent(); got != "wisp" {
		t.Errorf("Recent() = %q, want wisp", got)
	}
}

// With several, it is the one you last touched.
func TestRecentFollowsTheConversation(t *testing.T) {
	t.Setenv("HAUNT_RUNTIME_DIR", t.TempDir())
	listen(t, "wisp")
	listen(t, "moth")

	// Nobody has been spoken to yet: the caller should be told to choose
	// rather than handed a guess.
	if got := Recent(); got != "" {
		t.Errorf("Recent() = %q with nobody touched, want an empty answer", got)
	}

	Touch("wisp")
	if got := Recent(); got != "wisp" {
		t.Errorf("Recent() = %q after poking wisp", got)
	}
	// File mtimes are not always finer than this.
	time.Sleep(10 * time.Millisecond)
	Touch("moth")
	if got := Recent(); got != "moth" {
		t.Errorf("Recent() = %q after poking moth", got)
	}
}

// A ghost you spoke to yesterday and that is no longer running is not the
// answer; the one that is still there is.
func TestRecentIgnoresAGhostThatHasStopped(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HAUNT_RUNTIME_DIR", dir)
	s, err := Listen("wisp", func(proto.Event) error { return nil }, hlog.Discard())
	if err != nil {
		t.Fatal(err)
	}
	listen(t, "moth")
	Touch("wisp")
	time.Sleep(10 * time.Millisecond)
	Touch("moth")
	time.Sleep(10 * time.Millisecond)
	Touch("wisp")
	if got := Recent(); got != "wisp" {
		t.Fatalf("Recent() = %q, want wisp", got)
	}
	s.Close()
	Forget("wisp")
	if got := Recent(); got != "moth" {
		t.Errorf("Recent() = %q once wisp has stopped, want moth", got)
	}
}

// A socket file left by a crashed session is not a running ghost.
func TestRunningSkipsAStaleSocket(t *testing.T) {
	t.Setenv("HAUNT_RUNTIME_DIR", t.TempDir())
	path, err := SocketPath("crashed")
	if err != nil {
		t.Fatal(err)
	}
	if f, err := os.Create(path); err != nil {
		t.Fatal(err)
	} else {
		f.Close()
	}
	listen(t, "wisp")
	got := Running()
	if len(got) != 1 || got[0] != "wisp" {
		t.Errorf("Running() = %v, want only the live ghost", got)
	}
}
