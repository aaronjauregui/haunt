package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// Typing at a ghost, end to end: the gesture, the keys, the line, and the
// message event that comes out the other side.
//
// This is the test that could not exist before the harness had a keyboard
// (internal/testenv/keyboard.go). Both of the input line's real bugs so far
// lived in the gap it covers, and both were found by a person using the
// demo — the first because keyboard focus was surrendered to the pointer,
// the second because a leave meant for a closed line closed the next one.
func TestTypingAtTheGhost(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	rt := filepath.Join(dir, "run")
	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")

	gDir := filepath.Join(dir, "whisper")
	writeGhost(t, gDir, `[ghost]
name = "whisper"
capabilities = ["message"]

[brain]
wasm = "brain.wasm"
`)
	build(t, filepath.Join(gDir, "brain.wasm"), "../../examples/whisper", "GOOS=wasip1", "GOARCH=wasm")

	// The keyboard exists before the ghost does, so the seat advertises one
	// from the start — though the driver would pick it up either way, since
	// wl_seat capabilities can change while a client is connected.
	kb, err := c.NewKeyboard()
	if err != nil {
		t.Fatalf("virtual keyboard: %v", err)
	}
	defer kb.Close()

	const spriteX, spriteY = 200, 200
	record := filepath.Join(dir, "session.jsonl")
	cmd := exec.Command(bin, "run", "-x", "200", "-y", "200",
		"-state", filepath.Join(dir, "state"), "-record", record, gDir)
	cmd.Env = append(c.Env(), "HAUNT_RUNTIME_DIR="+rt, "HAUNT_DEBUG=input")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := &syncBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGTERM)
		cmd.Wait()
		if t.Failed() {
			t.Logf("ghost stderr:\n%s", stderr.String())
		}
	})
	waitFor(t, stdout, "READY")

	ptr, err := c.NewPointer()
	if err != nil {
		t.Fatal(err)
	}
	defer ptr.Close()
	rightClick := func() {
		t.Helper()
		if err := ptr.MoveTo(spriteX+80, spriteY+80); err != nil {
			t.Fatal(err)
		}
		if err := ptr.RightClick(); err != nil {
			t.Fatal(err)
		}
		waitLog(t, stderr, "input line opened")
	}
	typeLine := func(text string, key uint32) {
		t.Helper()
		if err := kb.Type(text); err != nil {
			t.Fatal(err)
		}
		if err := kb.Key(key); err != nil {
			t.Fatal(err)
		}
	}

	// A line, typed and sent. It arrives as an ordinary message event —
	// indistinguishable from `haunt say`, which is what lets brains handle
	// both without knowing the difference.
	rightClick()
	typeLine("hello ghost", keyEnter)
	waitRecordContains(t, record, `"text":"hello ghost"`)

	// Escape abandons what was typed: nothing is sent, and the line closes.
	rightClick()
	typeLine("forget this", keyEsc)
	waitLog(t, stderr, "input line closed")
	if data, _ := os.ReadFile(record); strings.Contains(string(data), "forget this") {
		t.Error("Escape sent the line anyway")
	}

	// And a line after a poke. This is the regression: closing a line makes
	// the compositor send a keyboard leave, and the leave for the closed
	// line used to close the next one the instant it opened — so the second
	// thing you ever said to your ghost went nowhere.
	if err := ptr.Press(); err != nil {
		t.Fatal(err)
	}
	if err := ptr.Release(); err != nil {
		t.Fatal(err)
	}
	waitRecordContains(t, record, `"type":"click"`)
	rightClick()
	typeLine("still listening", keyEnter)
	waitRecordContains(t, record, `"text":"still listening"`)

	// A held Backspace keeps deleting. Wayland leaves key repeat to the
	// client, and a driver that never implemented it deleted one
	// character however long the key was held. Held past the repeat delay
	// for long enough to eat the whole line, so only what follows is sent.
	rightClick()
	if err := kb.Type("scratch that"); err != nil {
		t.Fatal(err)
	}
	if err := kb.Hold(keyBackspace, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	typeLine("new thought", keyEnter)
	waitRecordContains(t, record, `"text":"new thought"`)
}

// Scancodes the test drives directly rather than through a character.
const (
	keyEsc       = 1
	keyBackspace = 14
	keyEnter     = 28
)

// The ghost holds the keyboard only while a line is open. Nothing else may
// take it: a companion that kept focus would swallow every shortcut on the
// desktop, which is the reason interactivity is on-demand at all.
func TestGhostHoldsTheKeyboardOnlyWhileTyping(t *testing.T) {
	c := testenv.StartT(t)
	dir := t.TempDir()
	rt := filepath.Join(dir, "run")
	bin := filepath.Join(dir, "haunt")
	build(t, bin, ".")

	gDir := filepath.Join(dir, "whisper")
	writeGhost(t, gDir, `[ghost]
name = "whisper"
capabilities = ["message"]

[brain]
wasm = "brain.wasm"
`)
	build(t, filepath.Join(gDir, "brain.wasm"), "../../examples/whisper", "GOOS=wasip1", "GOARCH=wasm")

	kb, err := c.NewKeyboard()
	if err != nil {
		t.Fatal(err)
	}
	defer kb.Close()

	const spriteX, spriteY = 200, 200
	record := filepath.Join(dir, "session.jsonl")
	cmd := exec.Command(bin, "run", "-x", "200", "-y", "200",
		"-state", filepath.Join(dir, "state"), "-record", record, gDir)
	cmd.Env = append(c.Env(), "HAUNT_RUNTIME_DIR="+rt, "HAUNT_DEBUG=input,wayland")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := &syncBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGTERM)
		cmd.Wait()
		if t.Failed() {
			t.Logf("ghost stderr:\n%s", stderr.String())
		}
	})
	waitFor(t, stdout, "READY")

	// Typing at a ghost that has not been asked to listen goes nowhere.
	if err := kb.Type("nobody asked"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if data, _ := os.ReadFile(record); strings.Contains(string(data), "nobody asked") {
		t.Error("the ghost was reading the keyboard without being asked to")
	}

	ptr, err := c.NewPointer()
	if err != nil {
		t.Fatal(err)
	}
	defer ptr.Close()
	if err := ptr.MoveTo(spriteX+80, spriteY+80); err != nil {
		t.Fatal(err)
	}
	if err := ptr.RightClick(); err != nil {
		t.Fatal(err)
	}
	waitLog(t, stderr, "keyboard enter")

	// It has focus now, and gives it up when the line ends. Asserted as
	// "the keys stop arriving" rather than "the compositor sent a leave":
	// with nothing else on screen to focus, sway is in no hurry about the
	// leave, and the ghost's behaviour is the part that matters.
	if err := kb.Key(keyEsc); err != nil {
		t.Fatal(err)
	}
	waitLog(t, stderr, "input line closed")
	if err := kb.Type("after escape"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if data, _ := os.ReadFile(record); strings.Contains(string(data), "after escape") {
		t.Error("the ghost kept reading the keyboard after the line closed")
	}
}
