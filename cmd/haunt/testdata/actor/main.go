// A brain that does one specific thing when told, for the agency tests: it
// is the `act` intent rather than the model's own tool calling, which is
// the path a brain takes when it wants a particular thing done at a
// particular moment.
package main

import "github.com/aaronjauregui/haunt/brainkit"

func main() {
	brainkit.Run(brainkit.Func(func(e brainkit.Event, emit *brainkit.Emitter) {
		switch e.Type {
		case "boot":
			// What it can do arrives with the boot event, so it is logged:
			// a brain that had to guess tool names would be guessing at a
			// driver that refuses the ones that do not exist.
			for _, t := range e.Tools {
				emit.Log("can use: " + t)
			}
		case "message":
			emit.Act("noting", "notes.append", map[string]any{"text": e.Text})
		case "acted":
			emit.Log("acted: " + e.Name + " -> " + e.Text)
		case "ai_error":
			emit.Log("refused: " + e.Text)
		}
	}))
}
