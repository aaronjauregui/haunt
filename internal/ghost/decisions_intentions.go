package ghost

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/aaronjauregui/haunt/internal/proto"
)

// Intentions: what the ghost takes on so it is not forgotten. In the
// store, listable, discharged by resolve rather than by coming due
// (intention.go).

func init() {
	register(verb{
		id: "intend",
		desc: "Take something on so it is not forgotten, across restarts. With delay_ms it is an errand that comes " +
			"back to you at that moment (a reminder); without, it is a goal you keep an eye on. Returns its name.",
		params:     []string{"text:string", "delay_ms?:integer", "name?:string"},
		run:        tool(toolIntend),
		sideEffect: true,
	})
	register(verb{
		id:         "resolve",
		desc:       "Discharge something you took on: it was done.",
		params:     []string{"name:string"},
		run:        tool(toolResolve),
		sideEffect: true,
	})
	register(verb{
		id:         "set_aside",
		desc:       "Stop keeping an eye on a goal the person no longer cares about, without claiming it was done.",
		params:     []string{"name:string"},
		run:        tool(toolSetAside),
		sideEffect: true,
	})
	register(verb{
		id:   "intentions",
		desc: "List what you are holding: errands and goals, with their names.",
		run:  tool(toolIntentions),
	})
}

// modelIntentionPrefix names the intentions the model takes on, so they
// cannot collide with the driver's own (goal checks, drift findings).
const modelIntentionPrefix = "model/"

func toolIntend(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Text    string `json:"text"`
		DelayMS int    `json:"delay_ms"`
		Name    string `json:"name"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	name := strings.TrimSpace(a.Name)
	if name == "" {
		name = fmt.Sprintf("%s%d", modelIntentionPrefix, time.Now().UnixNano())
	} else if !strings.HasPrefix(name, modelIntentionPrefix) {
		name = modelIntentionPrefix + name
	}
	in := proto.Intent{Type: proto.IntentIntend, Name: name, Text: a.Text, DelayMS: a.DelayMS}
	if err := in.Validate(); err != nil {
		return "", err
	}
	g.execIntend(in)
	if a.DelayMS > 0 {
		return name + " (due " + when(time.Now().Add(time.Duration(a.DelayMS)*time.Millisecond), time.Now()) + ")", nil
	}
	return name + " (a goal, watched)", nil
}

func toolResolve(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	name, err := intentionArg(g, args)
	if err != nil {
		return "", err
	}
	g.execResolve(name)
	return "resolved " + name, nil
}

func toolSetAside(g *Ghost, _ context.Context, args json.RawMessage) (string, error) {
	name, err := intentionArg(g, args)
	if err != nil {
		return "", err
	}
	g.execSetAside(name)
	return "set aside " + name, nil
}

// intentionArg reads and checks the name a resolve or set_aside is about.
func intentionArg(g *Ghost, args json.RawMessage) (string, error) {
	var a struct {
		Name string `json:"name"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	if _, ok := g.intentionNamed(a.Name); !ok {
		return "", fmt.Errorf("you are not holding anything called %q (see intentions)", a.Name)
	}
	return a.Name, nil
}

func toolIntentions(g *Ghost, _ context.Context, _ json.RawMessage) (string, error) {
	list := g.loadIntentions()
	if len(list) == 0 {
		return "You are not holding anything.", nil
	}
	now := time.Now()
	var b strings.Builder
	for _, it := range list {
		fmt.Fprintf(&b, "- %s: %s", it.Name, it.Text)
		switch {
		case it.Aside:
			b.WriteString(" (set aside)")
		case it.Fired:
			b.WriteString(" (due, not yet dealt with)")
		case !it.Goal():
			b.WriteString(" (due " + when(it.Due, now) + ")")
		default:
			b.WriteString(" (a goal)")
		}
		if it.Asked {
			b.WriteString(" [the person asked for this]")
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n"), nil
}
