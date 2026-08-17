package testenv

import (
	"strings"
	"testing"
	"time"
)

// The harness's own smoke test: a virtual keyboard can be created, and the
// compositor accepts a keymap and key events from it. If the protocol
// description this is generated from were wrong, sway would answer with a
// protocol error and the connection would die here rather than in some
// distant test of the ghost.
func TestVirtualKeyboardIsAccepted(t *testing.T) {
	c := StartT(t)

	kb, err := c.NewKeyboard()
	if err != nil {
		t.Fatalf("creating a virtual keyboard: %v", err)
	}
	defer kb.Close()

	if err := kb.Type("hello ghost"); err != nil {
		t.Fatalf("typing: %v", err)
	}
	if err := kb.Type("Shifted TEXT!"); err != nil {
		t.Fatalf("typing with shift: %v", err)
	}

	// And the seat now has a keyboard, which is the whole point: without
	// one the ghost can never hold keyboard focus, and nothing about the
	// input line is testable.
	out, err := c.Swaymsg("-t", "get_seats")
	if err != nil {
		t.Fatalf("get_seats: %v: %s", err, out)
	}
	if !strings.Contains(string(out), `"type": "keyboard"`) {
		t.Errorf("the seat still has no keyboard:\n%s", out)
	}
}

// A character the layout cannot produce is an error, not a silent gap.
func TestTypingWhatTheLayoutCannotProduce(t *testing.T) {
	c := StartT(t)
	kb, err := c.NewKeyboard()
	if err != nil {
		t.Fatal(err)
	}
	defer kb.Close()

	if err := kb.Type("café"); err == nil {
		t.Error("typing an unmappable character reported success")
	}
}

// Keys sent before the keymap are a protocol error; the harness must never
// be the thing that fails that way, so creation sends it eagerly.
func TestKeymapIsSentAtCreation(t *testing.T) {
	c := StartT(t)
	kb, err := c.NewKeyboard()
	if err != nil {
		t.Fatal(err)
	}
	defer kb.Close()

	// A key immediately after creation, with no other setup: if the keymap
	// had not gone out first the connection would be gone by the roundtrip.
	if err := kb.Key(30); err != nil { // 'a'
		t.Fatalf("first key rejected: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := kb.Key(30); err != nil {
		t.Fatalf("connection did not survive: %v", err)
	}
}
