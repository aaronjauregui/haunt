// Package textinput turns keyboard events into editable text: a line, or
// several when the person asks for a new one with a backslash before
// Enter.
//
// Keys arrive from Wayland as Linux evdev scancodes, and turning those into
// characters is properly the job of libxkbcommon — which is cgo, and the
// driver avoids cgo by design (see docs/dev/decisions.md). So this maps a US layout in
// pure Go: enough to type at your ghost, not enough for the world. Other
// layouts, dead keys, compose sequences and input methods all need the real
// thing, and the seam to add it later is Keymap.Rune.
package textinput

import "strings"

// Linux evdev scancodes, the subset a line editor cares about.
const (
	KeyEsc        = 1
	KeyBackspace  = 14
	KeyBackslash  = 43
	KeyEnter      = 28
	KeyLeftCtrl   = 29
	KeyLeftShift  = 42
	KeyRightShift = 54
	KeyLeftAlt    = 56
	KeySpace      = 57
	KeyKPEnter    = 96
	KeyRightCtrl  = 97
	KeyHome       = 102
	KeyUp         = 103
	KeyPageUp     = 104
	KeyLeft       = 105
	KeyRight      = 106
	KeyEnd        = 107
	KeyDown       = 108
	KeyPageDown   = 109
	KeyDelete     = 111
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

// Repeats reports whether holding a key should repeat it: Backspace,
// Delete, the arrows, the page keys and every key that types a character.
// Enter and Escape do not — a held Enter would send one line and then
// start on the next.
func Repeats(code uint32) bool {
	switch code {
	case KeyBackspace, KeyDelete, KeyLeft, KeyRight, KeyUp, KeyDown, KeyPageUp, KeyPageDown:
		return true
	}
	_, ok := usLayout[code]
	return ok
}

// Action is what a key press means to the editor beyond editing the text.
type Action int

const (
	ActionNone   Action = iota
	ActionSubmit        // Enter: hand the text to the ghost
	ActionCancel        // Escape: abandon the text
	// The keys that move what is shown above the text rather than the
	// text itself, which the editor has no use for: PageUp and PageDown a
	// page, Shift+Up and Shift+Down a few lines. Shift because it is the
	// one modifier the editor tracks, and plain Up and Down are the
	// editor's own, moving between lines.
	ActionPageBack
	ActionPageForward
	ActionScrollBack
	ActionScrollForward
)

// MaxLength bounds the text, so a stuck key cannot grow the buffer without
// limit or hand a model an enormous prompt. A newline counts as one.
const MaxLength = 500

// Editor is text being typed, with a cursor in it.
//
// Enter submits, unless the character before the cursor is a backslash:
// then the backslash goes and a newline takes its place, which is how a
// person writes more than one line in a box where Enter sends. A
// backslash anywhere else is a backslash, and one at the end of what is
// sent is sent.
type Editor struct {
	text  []rune
	shift bool
	// cursor is the rune index the next character goes in at, from 0 to
	// len(text).
	cursor int
}

// Key applies a key event. down distinguishes press from release; only
// presses edit, but shift state tracks both. The returned Action tells the
// caller what the key meant.
//
// Shift is tracked from the key events themselves rather than from
// wl_keyboard's modifier mask, which is expressed in xkb terms this package
// deliberately does not understand.
//
// Left and Right move the cursor a character, Home and End to the ends of
// its line, and Up and Down to the line above or below at the same column
// (or the end of a shorter one). On the first line Up, and on the last
// Down, do nothing, as they do in a single line.
func (e *Editor) Key(code uint32, down bool) Action {
	if IsShift(code) {
		e.shift = down
		return ActionNone
	}
	if !down {
		return ActionNone
	}
	e.cursor = min(max(e.cursor, 0), len(e.text))
	switch code {
	case KeyEnter, KeyKPEnter:
		if e.cursor > 0 && e.text[e.cursor-1] == '\\' {
			e.text[e.cursor-1] = '\n'
			return ActionNone
		}
		return ActionSubmit
	case KeyEsc:
		return ActionCancel
	case KeyBackspace:
		if e.cursor > 0 {
			e.text = append(e.text[:e.cursor-1], e.text[e.cursor:]...)
			e.cursor--
		}
		return ActionNone
	case KeyDelete:
		if e.cursor < len(e.text) {
			e.text = append(e.text[:e.cursor], e.text[e.cursor+1:]...)
		}
		return ActionNone
	case KeyLeft:
		e.cursor = max(e.cursor-1, 0)
		return ActionNone
	case KeyRight:
		e.cursor = min(e.cursor+1, len(e.text))
		return ActionNone
	case KeyHome:
		e.cursor = e.lineStart(e.cursor)
		return ActionNone
	case KeyEnd:
		e.cursor = e.lineEnd(e.cursor)
		return ActionNone
	case KeyUp:
		if e.shift {
			return ActionScrollBack
		}
		if start := e.lineStart(e.cursor); start > 0 {
			col := e.cursor - start
			prev := e.lineStart(start - 1)
			e.cursor = min(prev+col, start-1)
		}
		return ActionNone
	case KeyDown:
		if e.shift {
			return ActionScrollForward
		}
		if end := e.lineEnd(e.cursor); end < len(e.text) {
			col := e.cursor - e.lineStart(e.cursor)
			e.cursor = min(end+1+col, e.lineEnd(end+1))
		}
		return ActionNone
	case KeyPageUp:
		return ActionPageBack
	case KeyPageDown:
		return ActionPageForward
	}
	if r, ok := Rune(code, e.shift); ok && len(e.text) < MaxLength {
		e.text = append(e.text[:e.cursor], append([]rune{r}, e.text[e.cursor:]...)...)
		e.cursor++
	}
	return ActionNone
}

// lineStart is the index of the first rune of the line holding i.
func (e *Editor) lineStart(i int) int {
	for i > 0 && e.text[i-1] != '\n' {
		i--
	}
	return i
}

// lineEnd is the index of the newline ending the line holding i, or the
// end of the text on the last line.
func (e *Editor) lineEnd(i int) int {
	for i < len(e.text) && e.text[i] != '\n' {
		i++
	}
	return i
}

// Text is the text as typed, newlines and all.
func (e *Editor) Text() string { return string(e.text) }

// Cursor is the cursor's rune index into Text.
func (e *Editor) Cursor() int { return min(max(e.cursor, 0), len(e.text)) }

// Empty reports whether anything has been typed.
func (e *Editor) Empty() bool { return len(strings.TrimSpace(string(e.text))) == 0 }

// Reset clears the text, keeping modifier state — releasing shift after a
// submit must not leave the next line stuck in capitals.
func (e *Editor) Reset() { e.text, e.cursor = e.text[:0], 0 }
