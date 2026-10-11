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
		desc: "Take something on when they ask you to remind them or keep an eye on something. With delay_ms " +
			"it is a reminder that comes back to you then; without, a goal you watch for. Returns its name.",
		params:     []string{"text:string", "delay_ms?:integer", "name?:string"},
		run:        tool(toolIntend),
		sideEffect: true,
	})
	register(verb{
		id:         "resolve",
		desc:       "Close a reminder or goal that is done: they finished it, or you delivered the reminder.",
		params:     []string{"name:string"},
		run:        tool(toolResolve),
		sideEffect: true,
	})
	register(verb{
		id: "set_aside",
		desc: "Drop a goal they no longer want (\"I don't care about the marathon anymore\", \"drop it\") " +
			"without calling it done. name: its name or its quoted text.",
		params:     []string{"name:string"},
		run:        tool(toolSetAside),
		sideEffect: true,
	})
	register(verb{
		id:   "intentions",
		desc: "List the reminders and goals you hold, with the names resolve and set_aside take.",
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

// intentionArg reads the intention a resolve or set_aside is about. The
// model may give its name, its name without the `model/` the driver put
// on it, or its text as the turn context quotes it ("finish the thesis"):
// a model deciding without thinking it over reaches for the words, not
// the handle. Text counts only when one intention the model took on, and
// still holds, has it; never for the driver's own. Not
// found, the answer lists what is held, so the retry is a copy rather
// than another call to intentions.
func intentionArg(g *Ghost, args json.RawMessage) (string, error) {
	var a struct {
		Name string `json:"name"`
	}
	if err := decode(args, &a); err != nil {
		return "", err
	}
	want := strings.TrimSpace(a.Name)
	list := g.loadIntentions()
	for _, it := range list {
		if want != "" && (it.Name == want || it.Name == modelIntentionPrefix+want) {
			return it.Name, nil
		}
	}
	var byText, held []string
	for _, it := range list {
		if it.Aside {
			continue
		}
		held = append(held, fmt.Sprintf("%s (%q)", it.Name, it.Text))
		if want != "" && strings.HasPrefix(it.Name, modelIntentionPrefix) && strings.EqualFold(strings.TrimSpace(it.Text), want) {
			byText = append(byText, it.Name)
		}
	}
	if len(byText) == 1 {
		return byText[0], nil
	}
	if len(held) == 0 {
		return "", fmt.Errorf("you are not holding anything")
	}
	return "", fmt.Errorf("you are not holding anything called %q; you hold %s", want, strings.Join(held, ", "))
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
		case it.Note != nil:
			b.WriteString(" (a note on their desktop)")
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
