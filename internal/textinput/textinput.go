// Package textinput turns keyboard events into an editable line of text.
//
// Keys arrive from Wayland as Linux evdev scancodes, and turning those into
// characters is properly the job of libxkbcommon — which is cgo, and the
// driver avoids cgo by design (see DESIGN.md). So this maps a US layout in
// pure Go: enough to type at your ghost, not enough for the world. Other
// layouts, dead keys, compose sequences and input methods all need the real
// thing, and the seam to add it later is Keymap.Rune.
package textinput

import "strings"

// Linux evdev scancodes, the subset a line editor cares about.
const (
	KeyEsc        = 1
	KeyBackspace  = 14
	KeyEnter      = 28
	KeyLeftCtrl   = 29
	KeyLeftShift  = 42
	KeyRightShift = 54
	KeyLeftAlt    = 56
	KeySpace      = 57
	KeyKPEnter    = 96
	KeyRightCtrl  = 97
)

// usLayout maps scancode to its unshifted and shifted characters. Codes not
// present produce nothing — modifiers, function keys, the numeric keypad.
var usLayout = map[uint32][2]rune{
	2: {'1', '!'}, 3: {'2', '@'}, 4: {'3', '#'}, 5: {'4', '$'}, 6: {'5', '%'},
	7: {'6', '^'}, 8: {'7', '&'}, 9: {'8', '*'}, 10: {'9', '('}, 11: {'0', ')'},
	12: {'-', '_'}, 13: {'=', '+'},

	16: {'q', 'Q'}, 17: {'w', 'W'}, 18: {'e', 'E'}, 19: {'r', 'R'}, 20: {'t', 'T'},
	21: {'y', 'Y'}, 22: {'u', 'U'}, 23: {'i', 'I'}, 24: {'o', 'O'}, 25: {'p', 'P'},
	26: {'[', '{'}, 27: {']', '}'},

	30: {'a', 'A'}, 31: {'s', 'S'}, 32: {'d', 'D'}, 33: {'f', 'F'}, 34: {'g', 'G'},
	35: {'h', 'H'}, 36: {'j', 'J'}, 37: {'k', 'K'}, 38: {'l', 'L'},
	39: {';', ':'}, 40: {'\'', '"'}, 41: {'`', '~'}, 43: {'\\', '|'},

	44: {'z', 'Z'}, 45: {'x', 'X'}, 46: {'c', 'C'}, 47: {'v', 'V'}, 48: {'b', 'B'},
	49: {'n', 'N'}, 50: {'m', 'M'}, 51: {',', '<'}, 52: {'.', '>'}, 53: {'/', '?'},

	KeySpace: {' ', ' '},
}

// scancodes is usLayout read backwards: character to the key that produces
// it, and whether shift is held. Built from the same table so the two can
// never disagree — which matters because the only user is the test harness,
// typing at a ghost through a virtual keyboard to check that what comes out
// the other end is what was typed.
var scancodes = func() map[rune]struct {
	code  uint32
	shift bool
} {
	m := map[rune]struct {
		code  uint32
		shift bool
	}{}
	for code, pair := range usLayout {
		if _, seen := m[pair[0]]; !seen {
			m[pair[0]] = struct {
				code  uint32
				shift bool
			}{code, false}
		}
		if _, seen := m[pair[1]]; !seen {
			m[pair[1]] = struct {
				code  uint32
				shift bool
			}{code, true}
		}
	}
	return m
}()

// Scancode is Rune inverted: the key that produces r, and whether shift is
// held to get it.
func Scancode(r rune) (code uint32, shift bool, ok bool) {
	k, ok := scancodes[r]
	return k.code, k.shift, ok
}

// Rune returns the character a scancode produces, and whether it produces
// one at all.
func Rune(code uint32, shift bool) (rune, bool) {
	pair, ok := usLayout[code]
	if !ok {
		return 0, false
	}
	if shift {
		return pair[1], true
	}
	return pair[0], true
}

// IsShift reports whether a scancode is a shift key.
func IsShift(code uint32) bool {
	return code == KeyLeftShift || code == KeyRightShift
}

// Action is what a key press means to the editor beyond inserting text.
type Action int

const (
	ActionNone   Action = iota
	ActionSubmit        // Enter: hand the line to the ghost
	ActionCancel        // Escape: abandon the line
)

// MaxLength bounds a line, so a stuck key cannot grow the buffer without
// limit or hand a model an enormous prompt.
const MaxLength = 500

// Editor is one line being typed.
type Editor struct {
	text  []rune
	shift bool
}

// Key applies a key event. down distinguishes press from release; only
// presses edit, but shift state tracks both. The returned Action tells the
// caller what the key meant.
//
// Shift is tracked from the key events themselves rather than from
// wl_keyboard's modifier mask, which is expressed in xkb terms this package
// deliberately does not understand.
func (e *Editor) Key(code uint32, down bool) Action {
	if IsShift(code) {
		e.shift = down
		return ActionNone
	}
	if !down {
		return ActionNone
	}
	switch code {
	case KeyEnter, KeyKPEnter:
		return ActionSubmit
	case KeyEsc:
		return ActionCancel
	case KeyBackspace:
		if len(e.text) > 0 {
			e.text = e.text[:len(e.text)-1]
		}
		return ActionNone
	}
	if r, ok := Rune(code, e.shift); ok && len(e.text) < MaxLength {
		e.text = append(e.text, r)
	}
	return ActionNone
}

// Text is the line as typed.
func (e *Editor) Text() string { return string(e.text) }

// Empty reports whether anything has been typed.
func (e *Editor) Empty() bool { return len(strings.TrimSpace(string(e.text))) == 0 }

// Reset clears the line, keeping modifier state — releasing shift after a
// submit must not leave the next line stuck in capitals.
func (e *Editor) Reset() { e.text = e.text[:0] }
