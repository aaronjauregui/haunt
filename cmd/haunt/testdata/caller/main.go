// Caller is a test brain: shortly after boot it messages the whisper ghost.
package main

import "github.com/aaronjauregui/haunt/brainkit"

func main() {
	brainkit.Run(brainkit.Func(func(e brainkit.Event, emit *brainkit.Emitter) {
		switch e.Type {
		case "boot":
			emit.SetTimer("go", 300)
		case "timer":
			emit.Tell("whisper", "ping from caller")
		}
	}))
}
