package ghost

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/aaronjauregui/haunt/internal/model/fake"
	"github.com/aaronjauregui/haunt/internal/proto"
)

// A new decision is one type and an init that registers it: nothing else
// in the brain changes for the model to be offered it and for it to run.

// hum is a throwaway decision: the ghost hums, which nobody hears.
type hum struct{}

var hummed atomic.Int32

func (hum) name() string     { return "hum" }
func (hum) describe() string { return "Hum a tune to yourself. Nobody hears it." }
func (hum) terminal() bool   { return false }
func (hum) schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"tune":{"type":"string"}},"required":["tune"]}`)
}
func (hum) apply(_ context.Context, _ *Ghost, _ *turn, args json.RawMessage) (string, error) {
	hummed.Add(1)
	return "You hummed it.", nil
}

func init() { register(hum{}) }

func TestNewDecision(t *testing.T) {
	g, b, srv := modelGhost(t, 0,
		fake.Rule{Match: "hum", AfterTool: "hummed", Calls: []fake.Call{{Name: silentName}}},
		fake.Rule{Match: "hum", Calls: []fake.Call{{Name: "hum", Args: json.RawMessage(`{"tune":"greensleeves"}`)}}})
	g.markPrompted()
	b.Deliver(proto.Event{Type: proto.EventMessage, Text: "hum something"})
	waitTurns(t, b, 1)
	offered := false
	for _, tool := range requests(t, srv)[0].Tools {
		if tool.Function.Name == "hum" {
			offered = true
		}
	}
	if !offered {
		t.Error("hum was not among the tools sent")
	}
	if hummed.Load() != 1 {
		t.Errorf("hum ran %d times, want once", hummed.Load())
	}
}
