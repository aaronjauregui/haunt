package ghost

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
)

func whyGhost() *Ghost {
	return &Ghost{store: &memStore{m: map[string]string{}}, log: hlog.Discard()}
}

// The record is what makes the ghost interrogable, so it has to survive the
// round trip through the store intact.
func TestWhyRoundTrip(t *testing.T) {
	g := whyGhost()
	when := time.Date(2026, 3, 4, 9, 0, 0, 0, time.UTC)
	g.noteWhy(Why{
		Time:   when,
		User:   "what tea should I get",
		Reply:  "the smoky one, like last time",
		Acted:  []string{"look"},
		Window: WhyWindow{App: "firefox", Title: "tea shop"},
	})

	raw, ok, err := g.store.Get(WhyStoreKey)
	if err != nil || !ok {
		t.Fatalf("nothing was stored (%v)", err)
	}
	got, err := DecodeWhy(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("stored %d traces, want 1", len(got))
	}
	w := got[0]
	if w.Reply != "the smoky one, like last time" || w.User != "what tea should I get" {
		t.Errorf("the exchange did not survive: %+v", w)
	}
	if len(w.Acted) != 1 || w.Acted[0] != "look" {
		t.Errorf("the tools did not survive: %+v", w.Acted)
	}
	if w.Window.App != "firefox" || w.Window.Title != "tea shop" {
		t.Errorf("the window did not survive: %+v", w.Window)
	}
}

// The ring is short on purpose: the question is about what was just said.
func TestWhyIsBounded(t *testing.T) {
	g := whyGhost()
	for i := 0; i < whyKept+5; i++ {
		g.noteWhy(Why{Reply: "reply " + string(rune('a'+i))})
	}
	raw, _, _ := g.store.Get(WhyStoreKey)
	got, err := DecodeWhy(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != whyKept {
		t.Fatalf("kept %d traces, want the ring capped at %d", len(got), whyKept)
	}
	// The newest survive, not the oldest.
	if got[len(got)-1].Reply != "reply "+string(rune('a'+whyKept+4)) {
		t.Errorf("the newest trace is %q; the ring dropped the wrong end", got[len(got)-1].Reply)
	}
	if got[0].Reply == "reply a" {
		t.Error("the oldest trace survived a full ring")
	}
}

// A corrupt trace must cost the trace, not the conversation that produced it.
func TestWhySurvivesACorruptTrace(t *testing.T) {
	g := whyGhost()
	if err := g.store.Set(WhyStoreKey, "{not json"); err != nil {
		t.Fatal(err)
	}
	g.noteWhy(Why{Reply: "after the corruption"})
	raw, _, _ := g.store.Get(WhyStoreKey)
	got, err := DecodeWhy(raw)
	if err != nil {
		t.Fatalf("the trace did not recover: %v", err)
	}
	if len(got) != 1 || got[0].Reply != "after the corruption" {
		t.Errorf("got %+v, want just the new trace", got)
	}
}

// Whether the ghost could see the screen is part of the answer, and it must
// go blank when the capability does.
func TestWindowIsForgottenWhenNotWatching(t *testing.T) {
	g := whyGhost()
	if !g.currentWindow().Empty() {
		t.Error("a ghost that has never watched should know of no window")
	}
	g.noteWindow("foot", "vim ghost.go")
	if got := g.currentWindow(); got.App != "foot" || got.Title != "vim ghost.go" {
		t.Errorf("currentWindow = %+v", got)
	}
	g.noteWindow("", "")
	if !g.currentWindow().Empty() {
		t.Error("an ignored app should leave no window behind")
	}
}

// The stored shape is JSON in the ghost's own store, and the CLI decodes it
// without the ghost package's help beyond DecodeWhy.
func TestDecodeWhyRejectsGarbage(t *testing.T) {
	if _, err := DecodeWhy("[[["); err == nil {
		t.Error("garbage decoded as a trace")
	}
	var none []Why
	b, _ := json.Marshal(none)
	got, err := DecodeWhy(string(b))
	if err != nil || len(got) != 0 {
		t.Errorf("an empty trace should decode to nothing: %v %v", got, err)
	}
}
