// Teller is a test brain for the evals: a neighbour of wisp's that does, on
// cue, the two things a neighbour does. Told "tell", it tells wisp
// something, as one ghost to another; told "speak", it says something in
// public, which wisp sees as an observation. The cues come from the test
// (`haunt say -ghost moth speak`), so each lands when nothing else is in
// flight for wisp, rather than on a timer that might meet one of its turns.
package main

import "github.com/aaronjauregui/haunt/brainkit"

func main() {
	brainkit.Run(brainkit.Func(func(e brainkit.Event, emit *brainkit.Emitter) {
		if e.Type != "message" {
			return
		}
		switch e.Text {
		case "tell":
			emit.Tell("wisp", "did you hear that noise upstairs?")
		case "speak":
			emit.Say("Something is dripping inside the walls again.")
		}
	}))
}
