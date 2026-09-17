// Command spinkeys is a keyboard for scripts/hermes-spin.sh: it types at
// the demo's headless sway the way a person would, so a live spin can
// answer a ghost's questions (the ask and ask_text tools) instead of
// waiting two minutes for each one to lapse. Dev-only: it goes through the
// compositor's virtual keyboard, the same door internal/testenv's tests
// use, and the driver cannot tell it from a person.
//
// It reads commands from stdin, one per line, and says "ok" or "error: …"
// for each:
//
//	key <n>      press a digit, which picks row n of an open question
//	type <text>  type text (US layout)
//	enter        press Enter
//
// The keyboard is created once and kept for the whole run: a seat whose
// only keyboard comes and goes loses focus with it.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/aaronjauregui/haunt/internal/testenv"
)

// Evdev scancodes. The digits run 1..9 as codes 2..10.
const (
	keyOne   = 2
	keyEnter = 28
)

func main() {
	display := flag.String("display", os.Getenv("WAYLAND_DISPLAY"), "the compositor's WAYLAND_DISPLAY")
	flag.Parse()
	if *display == "" {
		fmt.Fprintln(os.Stderr, "spinkeys: no display (-display or WAYLAND_DISPLAY)")
		os.Exit(2)
	}
	kb, err := (&testenv.Compositor{Display: *display}).NewKeyboard()
	if err != nil {
		fmt.Fprintln(os.Stderr, "spinkeys:", err)
		os.Exit(1)
	}
	defer kb.Close()

	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		verb, arg, _ := strings.Cut(strings.TrimSpace(in.Text()), " ")
		if err := run(kb, verb, arg); err != nil {
			fmt.Println("error:", err)
			continue
		}
		fmt.Println("ok")
	}
}

func run(kb *testenv.Keyboard, verb, arg string) error {
	switch verb {
	case "key":
		n, err := strconv.Atoi(arg)
		if err != nil || n < 1 || n > 9 {
			return fmt.Errorf("key wants a digit from 1 to 9, not %q", arg)
		}
		return kb.Key(uint32(keyOne + n - 1))
	case "type":
		return kb.Type(arg)
	case "enter":
		return kb.Key(keyEnter)
	case "":
		return nil
	}
	return fmt.Errorf("unknown command %q", verb)
}
