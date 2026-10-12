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
		KeyDelete:    true,
		KeyLeft:      true,
		KeyDown:      true,
		KeyPageUp:    true,
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

// typeText presses the keys for s on the US layout, shift included.
func typeText(e *Editor, s string) {
	for _, r := range s {
		code, shift, ok := Scancode(r)
		if !ok {
			panic("no key for " + string(r))
		}
		if shift {
			e.Key(KeyLeftShift, true)
		}
		press(e, code)
		if shift {
			e.Key(KeyLeftShift, false)
		}
	}
}

// A backslash before the cursor turns Enter into a new line, and goes;
// Enter anywhere else still submits, and a backslash elsewhere stays.
func TestBackslashEnterIsANewLine(t *testing.T) {
	var e Editor
	typeText(&e, `one\`)
	if a := press(&e, KeyEnter); a != ActionNone {
		t.Fatalf("backslash-Enter = %v, want none", a)
	}
	typeText(&e, `two \ three`)
	if a := press(&e, KeyEnter); a != ActionSubmit {
		t.Fatalf("Enter after the second line = %v, want submit", a)
	}
	if got, want := e.Text(), "one\ntwo \\ three"; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
	typeText(&e, `\`)
	if a := press(&e, KeyKPEnter); a != ActionNone {
		t.Errorf("backslash-keypad Enter = %v, want none", a)
	}
	if got, want := e.Text(), "one\ntwo \\ three\n"; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
}

// The cursor moves within the text and typing goes in where it is.
func TestCursorEditing(t *testing.T) {
	var e Editor
	typeText(&e, "helo")
	press(&e, KeyLeft)
	typeText(&e, "l")
	press(&e, KeyEnd)
	typeText(&e, "!")
	press(&e, KeyHome)
	press(&e, KeyDelete)
	typeText(&e, "H")
	if got, want := e.Text(), "Hello!"; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
	// At the start Backspace and Left do nothing; at the end Right and
	// Delete do nothing.
	press(&e, KeyLeft)
	press(&e, KeyLeft)
	press(&e, KeyBackspace)
	if got := e.Text(); got != "Hello!" || e.Cursor() != 0 {
		t.Errorf("text = %q cursor %d, want %q at 0", got, e.Cursor(), "Hello!")
	}
	press(&e, KeyEnd)
	press(&e, KeyRight)
	press(&e, KeyDelete)
	if got := e.Text(); got != "Hello!" || e.Cursor() != len("Hello!") {
		t.Errorf("text = %q cursor %d, want %q at the end", got, e.Cursor(), "Hello!")
	}
}

// Up and Down move between lines at the same column, to the end of a
// shorter line, and do nothing on the first and last.
func TestUpDownMoveBetweenLines(t *testing.T) {
	var e Editor
	typeText(&e, "abcdef\\")
	press(&e, KeyEnter)
	typeText(&e, "xy\\")
	press(&e, KeyEnter)
	typeText(&e, "12345")
	// On the last line, column 5.
	if a := press(&e, KeyDown); a != ActionNone || e.Cursor() != len(e.text) {
		t.Errorf("Down on the last line: %v, cursor %d", a, e.Cursor())
	}
	press(&e, KeyUp) // "xy" is shorter: its end
	if got := e.Cursor(); got != len("abcdef\nxy") {
		t.Errorf("Up to a shorter line: cursor %d", got)
	}
	press(&e, KeyUp) // column 2 of "abcdef"
	if got := e.Cursor(); got != 2 {
		t.Errorf("Up to the first line: cursor %d, want 2", got)
	}
	if a := press(&e, KeyUp); a != ActionNone || e.Cursor() != 2 {
		t.Errorf("Up on the first line: %v, cursor %d", a, e.Cursor())
	}
	press(&e, KeyDown)
	press(&e, KeyDown)
	if got := e.Cursor(); got != len("abcdef\nxy\n12") {
		t.Errorf("Down twice: cursor %d", got)
	}
	typeText(&e, "_")
	if got, want := e.Text(), "abcdef\nxy\n12_345"; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
}

// The page keys and Shift with Up or Down are for the caller, and leave
// the text and cursor alone.
func TestScrollActions(t *testing.T) {
	var e Editor
	typeText(&e, "a\\")
	press(&e, KeyEnter)
	typeText(&e, "b")
	for code, want := range map[uint32]Action{KeyPageUp: ActionPageBack, KeyPageDown: ActionPageForward} {
		if a := press(&e, code); a != want {
			t.Errorf("key %d = %v, want %v", code, a, want)
		}
	}
	e.Key(KeyLeftShift, true)
	up, down := press(&e, KeyUp), press(&e, KeyDown)
	e.Key(KeyLeftShift, false)
	if up != ActionScrollBack || down != ActionScrollForward {
		t.Errorf("Shift+Up = %v, Shift+Down = %v", up, down)
	}
	if e.Text() != "a\nb" || e.Cursor() != 3 {
		t.Errorf("scrolling moved the text: %q cursor %d", e.Text(), e.Cursor())
	}
}
