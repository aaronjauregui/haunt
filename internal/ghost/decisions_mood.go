package ghost

import (
	"context"
	"encoding/json"
	"time"
)

// Mood: the ghost's one line about how it is feeling, kept until it sets
// another or the day ends (mood.go).

func init() {
	register(verb{
		id:         "mood",
		desc:       "Set how you are feeling, in one short line, when it changes. It colours later turns and clears overnight.",
		params:     []string{"text:string"},
		run:        tool(toolMood),
		sideEffect: true,
	})
}

func toolMood(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Text string `json:"text"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	m, err := g.execMood(a.Text, time.Now())
	if err != nil {
		return "", err
	}
	return "Your mood is now: " + m.Text, nil
}
