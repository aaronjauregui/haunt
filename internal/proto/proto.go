// Package proto defines the event and intent schema shared by the driver,
// the event bus, and ghost brains. Events describe what happened; intents
// describe what a brain wants done. Both are JSON-serializable so they can
// cross the WASM boundary, the brain socket protocol, and session
// recordings unchanged.
package proto

import (
	"encoding/json"
	"fmt"
	"time"
)

// EventType enumerates everything that can happen to a ghost.
type EventType string

const (
	// Lifecycle.
	EventBoot     EventType = "boot"     // ghost loaded, brain should greet/init
	EventShutdown EventType = "shutdown" // driver is stopping the ghost

	// Interaction, sprite-local coordinates.
	EventClick     EventType = "click" // press+release on an opaque pixel
	EventDragStart EventType = "drag_start"
	EventDragEnd   EventType = "drag_end" // includes final position
	EventHover     EventType = "hover"    // pointer entered opaque area
	EventUnhover   EventType = "unhover"

	// Time.
	EventTick  EventType = "tick"  // periodic driver heartbeat
	EventTimer EventType = "timer" // a timer the brain scheduled fired

	// External.
	EventMessage EventType = "message" // inbound text (chat, CLI, socket)
)

// Event is one occurrence delivered to a brain.
type Event struct {
	// Seq is assigned by the bus, monotonically increasing per session.
	Seq uint64 `json:"seq"`
	// Time is when the event entered the bus.
	Time time.Time `json:"time"`
	Type EventType `json:"type"`

	// X, Y are sprite-local coordinates for interaction events.
	X int `json:"x,omitempty"`
	Y int `json:"y,omitempty"`
	// Name identifies a timer for EventTimer.
	Name string `json:"name,omitempty"`
	// Text carries message payloads for EventMessage.
	Text string `json:"text,omitempty"`
	// KV is the ghost's persisted state, included only with EventBoot so
	// brains start with their memory in hand.
	KV map[string]string `json:"kv,omitempty"`
}

// IntentType enumerates what a brain may request.
type IntentType string

const (
	IntentSay         IntentType = "say"       // show balloon text
	IntentHideSay     IntentType = "hide_say"  // dismiss the balloon
	IntentMove        IntentType = "move"      // reposition the sprite
	IntentSetTimer    IntentType = "set_timer" // fire EventTimer after delay
	IntentCancelTimer IntentType = "cancel_timer"
	IntentKVSet       IntentType = "kv_set" // persist a value
	IntentLog         IntentType = "log"    // debug output
)

// Intent is one request emitted by a brain. The driver validates and
// executes intents; brains have no other way to affect the world.
type Intent struct {
	Type IntentType `json:"type"`

	// Text is the balloon markup for IntentSay, the message for IntentLog.
	Text string `json:"text,omitempty"`
	// X, Y are absolute output coordinates for IntentMove.
	X int `json:"x,omitempty"`
	Y int `json:"y,omitempty"`
	// Name identifies timers and KV keys.
	Name string `json:"name,omitempty"`
	// DelayMS is the timer delay for IntentSetTimer.
	DelayMS int `json:"delay_ms,omitempty"`
	// Value is the payload for IntentKVSet.
	Value string `json:"value,omitempty"`
}

// EncodeEvents marshals events one-per-line (JSONL) for recordings.
func EncodeEvents(events []Event) ([]byte, error) {
	var out []byte
	for _, e := range events {
		b, err := json.Marshal(e)
		if err != nil {
			return nil, err
		}
		out = append(out, b...)
		out = append(out, '\n')
	}
	return out, nil
}

// Validate rejects intents the driver must not execute.
func (i *Intent) Validate() error {
	switch i.Type {
	case IntentSay:
		if i.Text == "" {
			return fmt.Errorf("say intent with empty text")
		}
	case IntentSetTimer:
		if i.Name == "" {
			return fmt.Errorf("set_timer intent with empty name")
		}
		if i.DelayMS <= 0 {
			return fmt.Errorf("set_timer intent with non-positive delay %d", i.DelayMS)
		}
	case IntentCancelTimer:
		if i.Name == "" {
			return fmt.Errorf("cancel_timer intent with empty name")
		}
	case IntentKVSet:
		if i.Name == "" {
			return fmt.Errorf("kv_set intent with empty key")
		}
	case IntentHideSay, IntentMove, IntentLog:
	default:
		return fmt.Errorf("unknown intent type %q", i.Type)
	}
	return nil
}
