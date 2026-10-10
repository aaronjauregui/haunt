package ghost

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aaronjauregui/haunt/internal/control"
	"github.com/aaronjauregui/haunt/internal/proto"
)

// Speech: the two decisions that end a turn, and the ways of talking to
// the person and to the other ghosts before it ends.

const (
	speakName  = "speak"
	silentName = "stay_silent"
)

// speakCue is speak's result: what the model reads just before it writes
// the words, in the speak call.
const speakCue = "Now say it: only the words for your balloon."

// saidCue is speak's result when the words came with it, folded: there is
// nothing more to write.
const saidCue = "Said."

// speakVerb is speak with a description. Unfolded, the words come in a
// call of their own after it; folded, they are the text of the reply that
// calls it, and offered puts foldedSpeak in its place.
func speakVerb(desc string) verb {
	return verb{
		id:     speakName,
		desc:   desc,
		params: []string{"pose?:string"},
		ends:   true,
		run: func(context.Context, *Ghost, *turn, json.RawMessage) (string, error) {
			return speakCue, nil
		},
	}
}

// foldedSpeak is speak as offered with fold on.
var foldedSpeak = speakVerb("Say something to the person: write the words as the text of this same reply " +
	"and call speak in it, with pose the face you wear as you say them (one of your poses, or empty to let your words choose).")

func init() {
	register(speakVerb("Say something to the person. pose is the face you wear as you say it, one of your poses, " +
		"or empty to let your words choose. You write the words next."))
	register(verb{
		id:   silentName,
		desc: "Say nothing this turn. Most events deserve no reply; nothing is shown.",
		ends: true,
		run: func(context.Context, *Ghost, *turn, json.RawMessage) (string, error) {
			return "You said nothing.", nil
		},
	})
	register(verb{
		id:     "say",
		desc:   "Show a line in your speech balloon now, before you finish deciding. Rarely needed.",
		params: []string{"text:string", "linger_ms?:integer"},
		run:    tool(toolSay),
	})
	register(verb{
		id: "ask",
		desc: "Ask the person a question with 1 to 9 choices and wait for their pick. " +
			"Returns the chosen text, or an empty string if they dismissed it.",
		params: []string{"text:string", "choices:array"},
		run:    tool(toolAsk),
	})
	register(verb{
		id:     "ask_text",
		desc:   "Ask the person to type an answer and wait for it. Returns what they typed, or empty if they did not.",
		params: []string{"prompt:string"},
		run:    tool(toolAskText),
	})
	register(verb{
		id:         "tell",
		desc:       "Say something to another ghost in the room, by name.",
		params:     []string{"ghost:string", "text:string"},
		run:        tool(toolTell),
		sideEffect: true,
	})
}

func toolSay(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Text     string `json:"text"`
		LingerMS int    `json:"linger_ms"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	text := strings.TrimSpace(a.Text)
	if text == "" {
		return "", fmt.Errorf("nothing to say")
	}
	if muted, why := g.quiet.Active(); muted {
		return "", refuse("it is quiet time (%s)", why)
	}
	g.showPaged(text)
	g.talkFor(text)
	if a.LingerMS > 0 {
		g.lingerBalloon(g.balloonGen(), time.Duration(a.LingerMS)*time.Millisecond)
	}
	return "shown", nil
}

func toolAsk(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Text    string   `json:"text"`
		Choices []string `json:"choices"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Text) == "" {
		return "", fmt.Errorf("ask needs text, the question, and choices, 1 to 9 of them")
	}
	if len(a.Choices) < 1 || len(a.Choices) > 9 {
		return "", fmt.Errorf("between 1 and 9 choices, please")
	}
	if !g.canAskNow() {
		return "", refuse("there is no way to ask the person right now")
	}
	return g.ask(a.Text, a.Choices...), nil
}

func toolAskText(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Prompt string `json:"prompt"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if !g.canAskNow() {
		return "", refuse("there is no way to ask the person right now")
	}
	return g.askForText(a.Prompt), nil
}

// canAskNow is whether a question can reach anyone: not in a quiet hour, and
// not without a screen to put it on.
func (g *Ghost) canAskNow() bool {
	if muted, _ := g.quiet.Active(); muted {
		return false
	}
	return g.surface != nil
}

func toolTell(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Ghost string `json:"ghost"`
		Text  string `json:"text"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if a.Ghost == "" || strings.TrimSpace(a.Text) == "" {
		return "", fmt.Errorf("tell whom, what?")
	}
	if a.Ghost == g.manifest.Ghost.Name {
		return "", fmt.Errorf("that is you")
	}
	if !g.ensureCapability("tell") {
		return "", refuse("the tell capability is not granted")
	}
	if err := control.Send(a.Ghost, proto.Event{Type: proto.EventMessage, Text: a.Text, From: g.manifest.Ghost.Name}); err != nil {
		return "", err
	}
	return "told " + a.Ghost, nil
}
