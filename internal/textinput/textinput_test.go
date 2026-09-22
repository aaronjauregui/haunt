package textinput

import "testing"

// press types a scancode: down then up, as a real keyboard reports it.
func press(e *Editor, code uint32) Action {
	a := e.Key(code, true)
	e.Key(code, false)
	return a
}

func TestTyping(t *testing.T) {
	var e Editor
	// "hi you"
	for _, c := range []uint32{35, 23, KeySpace, 21, 24, 22} {
		if a := press(&e, c); a != ActionNone {
			t.Fatalf("scancode %d produced action %v", c, a)
		}
	}
	if got := e.Text(); got != "hi you" {
		t.Errorf("text = %q, want %q", got, "hi you")
	}
}

func TestShift(t *testing.T) {
	var e Editor
	e.Key(KeyLeftShift, true)
	press(&e, 35) // h
	press(&e, 23) // i
	e.Key(KeyLeftShift, false)
	press(&e, 33) // f
	e.Key(KeyRightShift, true)
	press(&e, 3) // 2 -> @
	e.Key(KeyRightShift, false)

	if got, want := e.Text(), "HIf@"; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
}

func TestBackspace(t *testing.T) {
	var e Editor
	press(&e, 30) // a
	press(&e, 31) // s
	press(&e, KeyBackspace)
	if got := e.Text(); got != "a" {
		t.Errorf("text = %q, want %q", got, "a")
	}
	// Backspacing an empty line is not an error, and not an underflow.
	press(&e, KeyBackspace)
	press(&e, KeyBackspace)
	if got := e.Text(); got != "" {
		t.Errorf("text = %q, want empty", got)
	}
}

func TestActions(t *testing.T) {
	var e Editor
	if a := press(&e, KeyEnter); a != ActionSubmit {
		t.Errorf("enter = %v, want submit", a)
	}
	if a := press(&e, KeyKPEnter); a != ActionSubmit {
		t.Errorf("keypad enter = %v, want submit", a)
	}
	if a := press(&e, KeyEsc); a != ActionCancel {
		t.Errorf("escape = %v, want cancel", a)
	}
	// Key releases never act: holding enter must not submit twice.
	if a := e.Key(KeyEnter, false); a != ActionNone {
		t.Errorf("enter release = %v, want none", a)
	}
}

func TestUnmappedKeysAreIgnored(t *testing.T) {
	var e Editor
	for _, code := range []uint32{KeyLeftCtrl, KeyLeftAlt, 59 /* F1 */, 125 /* super */} {
		press(&e, code)
	}
	if got := e.Text(); got != "" {
		t.Errorf("modifier keys typed %q", got)
	}
}

func TestMaxLength(t *testing.T) {
	var e Editor
	for i := 0; i < MaxLength+50; i++ {
		press(&e, 30) // a
	}
	if got := len([]rune(e.Text())); got != MaxLength {
		t.Errorf("length = %d, want it capped at %d", got, MaxLength)
	}
}

func TestEmptyAndReset(t *testing.T) {
	var e Editor
	if !e.Empty() {
		t.Error("a new editor is not empty")
	}
	press(&e, KeySpace)
	press(&e, KeySpace)
	if !e.Empty() {
		t.Error("whitespace counts as content")
	}
	press(&e, 30)
	if e.Empty() {
		t.Error("typed text counts as empty")
	}

	// Reset keeps shift state: releasing shift after submitting must not
	// leave the next line stuck in capitals.
	e.Key(KeyLeftShift, true)
	e.Reset()
	press(&e, 30)
	if got := e.Text(); got != "A" {
		t.Errorf("after reset with shift held, typed %q, want %q", got, "A")
	}
}

func TestRepeats(t *testing.T) {
	for code, want := range map[uint32]bool{
		KeyBackspace: true,
		30:           true, // a
		KeySpace:     true,
		KeyEnter:     false,
		KeyEsc:       false,
		KeyLeftShift: false,
	} {
		if got := Repeats(code); got != want {
			t.Errorf("Repeats(%d) = %v, want %v", code, got, want)
		}
	}
}
