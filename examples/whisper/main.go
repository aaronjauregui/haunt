// Whisper is the example Haunt brain: a small ghost that greets, counts
// visits across sessions, and reacts to clicks and chat. Build it for the
// sandbox with: GOOS=wasip1 GOARCH=wasm go build -o brain.wasm ./examples/whisper
package main

import (
	"fmt"
	"strconv"

	"github.com/aaronjauregui/haunt/brainkit"
)

type whisper struct {
	visits int
	clicks int
}

func (w *whisper) OnEvent(e brainkit.Event, emit *brainkit.Emitter) {
	switch e.Type {
	case "boot":
		w.visits, _ = strconv.Atoi(e.KV["visits"])
		w.visits++
		emit.Remember("visits", strconv.Itoa(w.visits))
		if w.visits == 1 {
			emit.Say("Oh! A new haunt. Hello.")
		} else {
			emit.Say(fmt.Sprintf("Back again — visit %d.", w.visits))
		}
		emit.SetTimer("settle", 4000)
	case "timer":
		if e.Name == "settle" {
			emit.HideSay()
		}
	case "click":
		w.clicks++
		switch w.clicks % 3 {
		case 1:
			emit.Say("Boo.")
		case 2:
			emit.Say("Careful, I'm translucent.")
		default:
			emit.Say("That tickles.")
		}
		emit.SetTimer("settle", 4000)
	case "message":
		emit.Say("You said: " + e.Text)
		emit.SetTimer("settle", 6000)
	}
}

func main() {
	brainkit.Run(&whisper{})
}
