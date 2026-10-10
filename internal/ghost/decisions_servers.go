package ghost

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aaronjauregui/haunt/internal/model"
)

// Server tools: what the package's [[tool]] servers offer, as decisions.
// They are built each turn from the running servers (decide.go, offered),
// since a server can start mid-session when the person grants `tools`.
// Consent and the budget stay the driver's however the model reaches
// them (act.go), and every one of them is side-effecting by construction:
// somebody else's program, doing something in the world.

// serverDecision is one server tool, by the name the model calls it.
type serverDecision struct {
	spec model.Tool
	ref  toolRef
}

func (s serverDecision) name() string     { return s.spec.Name }
func (s serverDecision) describe() string { return s.spec.Description }
func (s serverDecision) terminal() bool   { return false }
func (s serverDecision) schema() json.RawMessage {
	if len(s.spec.Schema) == 0 {
		return json.RawMessage(`{"type":"object"}`)
	}
	return s.spec.Schema
}

func (s serverDecision) apply(_ context.Context, g *Ghost, t *turn, args json.RawMessage) (string, error) {
	return g.serverTool(t, s.ref, args)
}

// serverTool runs one of the ghost's declared servers' tools.
func (g *Ghost) serverTool(t *turn, ref toolRef, args json.RawMessage) (string, error) {
	if !g.declares("tools") {
		return "", refuse("there is no tool called %s", wireName(ref))
	}
	if !g.ensureCapability("tools") {
		return "", refuse("the tools capability is not granted")
	}
	if t != nil {
		if err := g.allowAfterReading(t, actSummary(ref.String(), args), taintAny); err != nil {
			return "", err
		}
	}
	out := g.runTool(ref, args, "")
	if out.Refused {
		return "", refuse("%s", out.Text)
	}
	text := g.readable(ref, out.Text)
	if out.Failed {
		return "", fmt.Errorf("%s", text)
	}
	return text, nil
}
