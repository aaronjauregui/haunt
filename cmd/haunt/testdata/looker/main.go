// A brain that looks at the screen when asked, for the awareness tests.
package main

import "github.com/aaronjauregui/haunt/brainkit"

func main() {
	brainkit.Run(brainkit.Func(func(e brainkit.Event, emit *brainkit.Emitter) {
		switch e.Type {
		case "message":
			emit.Look("peek", "")
		case "ai_look":
			// Noted, not spoken: what the ghost does with having looked is
			// its own business.
			emit.Log("saw: " + e.Text)
		}
	}))
}
