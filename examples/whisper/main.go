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
			emit.SetExpression("neutral")
		}
	case "click":
		w.clicks++
		// Shells that declare regions say where they were poked; ones that
		// do not send an empty region, so this still works on a flat shell.
		if e.Region == "face" {
			emit.Say("Not the face.")
			emit.SetExpression("sad")
			emit.SetTimer("settle", 4000)
			return
		}
		switch w.clicks % 3 {
		case 1:
			emit.Say("Boo.")
		case 2:
			emit.Say("Careful, I'm translucent.")
		default:
			emit.Say("That tickles.")
		}
		emit.SetExpression("happy")
		emit.SetTimer("settle", 4000)
	case "message":
		emit.Say("You said: " + e.Text)
		// No expression set here on purpose: the driver infers one from
		// the line itself, which is the interesting path to demo.
		emit.SetTimer("settle", 6000)
	}
}

func main() {
	brainkit.Run(&whisper{})
}
