// A brain that asks questions, for the balloon-menu end-to-end test: it
// offers a menu whenever it is poked or spoken to, and says what came back.
package main

import "github.com/aaronjauregui/haunt/brainkit"

func main() {
	brainkit.Run(brainkit.Func(func(e brainkit.Event, emit *brainkit.Emitter) {
		switch e.Type {
		case "boot", "click", "message":
			emit.Offer("menu", "What would you like?", "tea", "coffee", "nothing")
		case "choice":
			if e.Name == "" {
				emit.Say("dismissed:" + e.Tag)
				return
			}
			emit.Say("picked:" + e.Name)
		}
	}))
}
