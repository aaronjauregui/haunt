// Muse is the AI example ghost: chat messages go to the user's configured
// LLM in Muse's persona, and "draw ..." messages become images.
// Build: GOOS=wasip1 GOARCH=wasm go build -o brain.wasm ./examples/muse
package main

import (
	"strings"

	"github.com/aaronjauregui/haunt/brainkit"
)

func main() {
	brainkit.Run(brainkit.Func(func(e brainkit.Event, emit *brainkit.Emitter) {
		switch e.Type {
		case "boot":
			emit.Say("Muse here. Talk to me with `haunt say`, or say `draw <something>`.")
			emit.SetTimer("hush", 6000)
		case "timer":
			if e.Name == "hush" {
				emit.HideSay()
			}
		case "message":
			if prompt, ok := strings.CutPrefix(e.Text, "draw "); ok {
				emit.Say("Painting…")
				emit.Imagine(prompt)
				return
			}
			emit.Chat(e.Text)
		case "ai_reply":
			// The driver already streamed it to the balloon; linger a while.
			emit.SetTimer("hush", 12000)
		case "ai_image":
			emit.SetTimer("hush", 20000)
		case "ai_error":
			emit.Say("(my muse failed me: " + e.Text + ")")
			emit.SetTimer("hush", 8000)
		}
	}))
}
