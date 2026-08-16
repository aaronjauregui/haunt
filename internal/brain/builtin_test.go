package brain

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"testing"

	"github.com/aaronjauregui/haunt/internal/proto"
)

// echoBrain is a minimal brain in the same shape brainkit gives one: read
// event lines, write intent lines. The test uses its own rather than
// importing brainkit, so this stays a test of the host.
func echoBrain(events io.Reader, intents io.Writer) {
	sc := bufio.NewScanner(events)
	enc := json.NewEncoder(intents)
	for sc.Scan() {
		var e proto.Event
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		enc.Encode(proto.Intent{Type: proto.IntentSay, Text: string(e.Type) + ":" + e.Text})
	}
}

func TestBuiltinHostRoundTrip(t *testing.T) {
	col := newCollector()
	h, err := StartBuiltin(echoBrain, col.handle, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Deliver(proto.Event{Type: proto.EventMessage, Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	intents := col.waitFor(t, "the echo", func(in []proto.Intent) bool { return len(in) >= 1 })
	if intents[0].Text != "message:hello" {
		t.Errorf("intent = %+v, want the event echoed back", intents[0])
	}
	// Closing the event pipe is what ends a brain's read loop, for a
	// built-in one exactly as for a WASM one.
	if err := h.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

// The point of hosting the built-in brain over a pipe rather than calling
// into it: its intents go through the same parsing and validation as a
// downloaded brain's, so it cannot smuggle a malformed one past the driver.
func TestBuiltinIntentsAreValidated(t *testing.T) {
	col := newCollector()
	h, err := StartBuiltin(func(events io.Reader, intents io.Writer) {
		io.WriteString(intents, "{\"type\":\"say\"}\n")                 // no text: invalid
		io.WriteString(intents, "{\"type\":\"nonsense\"}\n")            // unknown type
		io.WriteString(intents, "not json at all\n")                    // unparseable
		io.WriteString(intents, "{\"type\":\"say\",\"text\":\"ok\"}\n") // fine
		io.Copy(io.Discard, events)
	}, col.handle, Options{Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	intents := col.waitFor(t, "the one valid intent", func(in []proto.Intent) bool { return len(in) >= 1 })
	if len(intents) != 1 || intents[0].Text != "ok" {
		t.Errorf("intents = %+v, want only the valid one", intents)
	}
	h.Close()
}

// A built-in brain that panics takes itself down, not the session: the
// ghost falls silent, which is what a crashed WASM brain does too.
func TestBuiltinPanicIsContained(t *testing.T) {
	var log bytes.Buffer
	col := newCollector()
	h, err := StartBuiltin(func(io.Reader, io.Writer) { panic("boom") }, col.handle, Options{Stderr: &log})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Errorf("close after panic: %v", err)
	}
	if !bytes.Contains(log.Bytes(), []byte("boom")) {
		t.Errorf("the panic was swallowed silently; log = %q", log.String())
	}
	// Delivering to a dead brain fails rather than blocking forever.
	if err := h.Deliver(proto.Event{Type: proto.EventClick}); err == nil {
		t.Error("delivering to a stopped brain reported success")
	}
}
