// Package proto defines the event and intent schema shared by the driver,
// the event bus, and ghost brains. Events describe what happened; intents
// describe what a brain wants done. Both are JSON-serializable so they can
// cross the WASM boundary, the brain socket protocol, and session
// recordings unchanged.
package proto

import (
	"encoding/json"
	"fmt"
	"strings"
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

	// System.
	EventCron   EventType = "cron"   // a manifest-scheduled trigger fired
	EventIdle   EventType = "idle"   // user went idle
	EventActive EventType = "active" // user came back
	// EventWindow reports the focused window changing: Name is the
	// application, Text its title. Requires the window capability.
	EventWindow EventType = "window"

	// AI results, delivered after chat/imagine intents.
	EventAIReply EventType = "ai_reply" // Text holds the full reply
	EventAIImage EventType = "ai_image" // Name holds the cached image path
	EventAIError EventType = "ai_error" // Text holds the failure reason
	// EventAILook answers a look intent: Text is what the vision model
	// said, Name the application it was shown.
	EventAILook EventType = "ai_look"

	// EventChoice answers a say intent that offered choices: Name is the
	// option the user picked, and Tag echoes the intent's. An empty Name
	// means the menu was dismissed without choosing — a brain waiting on an
	// answer has to hear that too, or it waits forever.
	EventChoice EventType = "choice"
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
	// From identifies the sending ghost for cross-ghost messages.
	From string `json:"from,omitempty"`
	// Tag echoes the tag of the intent this event answers, so a brain that
	// has several AI requests in flight can tell their results apart.
	Tag string `json:"tag,omitempty"`
	// Region names the part of the shell an interaction landed on ("head",
	// "hand"), when the shell declares regions. Empty otherwise, so a brain
	// may ignore it entirely.
	Region string `json:"region,omitempty"`
	// KV is the ghost's persisted state, included only with EventBoot so
	// brains start with their memory in hand.
	KV map[string]string `json:"kv,omitempty"`
}

// IntentType enumerates what a brain may request.
type IntentType string

const (
	IntentSay           IntentType = "say"       // show balloon text
	IntentHideSay       IntentType = "hide_say"  // dismiss the balloon
	IntentMove          IntentType = "move"      // reposition the sprite
	IntentSetTimer      IntentType = "set_timer" // fire EventTimer after delay
	IntentCancelTimer   IntentType = "cancel_timer"
	IntentKVSet         IntentType = "kv_set"         // persist a value
	IntentLog           IntentType = "log"            // debug output
	IntentTell          IntentType = "tell"           // message another ghost
	IntentChat          IntentType = "chat"           // LLM reply to Text, streamed to the balloon
	IntentImagine       IntentType = "imagine"        // generate an image of Text
	IntentNote          IntentType = "note"           // journal a self-note; recent notes join LLM context
	IntentSetExpression IntentType = "set_expression" // wear a declared pose
	IntentPlayClip      IntentType = "play_clip"      // run a declared animation
	IntentLook          IntentType = "look"           // show the focused window to a vision model
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
	// Tag labels an AI request; the resulting ai_reply/ai_image/ai_error
	// event carries it back.
	Tag string `json:"tag,omitempty"`
	// Silent suppresses the balloon for IntentChat and keeps the exchange
	// out of conversation history — for brain-internal LLM calls such as
	// rewriting a user request into an image prompt.
	Silent bool `json:"silent,omitempty"`
	// SelfPrompt marks IntentChat text as a stage direction the ghost gave
	// itself rather than something the user said. The model still receives
	// it — that is how the ghost is steered — but history and memory record
	// only the reply, and record it as unprompted. Without this, a ghost
	// that speaks first slowly fills its own memory with utterances it
	// attributes to a user who never made them.
	SelfPrompt bool `json:"self_prompt,omitempty"`
	// Choices turn an IntentSay into a question: the balloon shows the text
	// and then these options, and the one the user picks comes back as an
	// EventChoice carrying the intent's Tag.
	//
	// Menus are how classic ukagaka ghosts are mostly *operated*; without
	// them the only ways to address a ghost are clicking it and typing free
	// text at it. They are also where consent belongs — asked in the moment,
	// about a thing the person can see.
	Choices []string `json:"choices,omitempty"`
	// Fresh skips the generation cache for IntentImagine, so asking again
	// for the same prompt produces a different image.
	Fresh bool `json:"fresh,omitempty"`
	// Wait is what the ghost says while an IntentChat or IntentImagine is
	// still being answered — "thinking…", "let me look that up". The driver
	// draws the waiting indicator itself, on its own delay, so a brain that
	// supplies nothing still shows one; this is the character's words for
	// it, which are the brain's business and nobody else's.
	Wait string `json:"wait,omitempty"`
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

// maxChoices is how many options a balloon will offer. Nine, because the
// keyboard path picks by digit and there is no tenth digit.
const maxChoices = 9

// Validate rejects intents the driver must not execute.
func (i *Intent) Validate() error {
	switch i.Type {
	case IntentSay:
		if i.Text == "" {
			return fmt.Errorf("say intent with empty text")
		}
		// A menu of one is a statement, and a menu of twenty is a wall: the
		// balloon has to fit on a screen beside a ghost, and the keyboard
		// path selects by digit.
		if len(i.Choices) > maxChoices {
			return fmt.Errorf("say intent with %d choices; at most %d fit in a balloon", len(i.Choices), maxChoices)
		}
		for n, c := range i.Choices {
			if strings.TrimSpace(c) == "" {
				return fmt.Errorf("say intent with an empty choice at position %d", n+1)
			}
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
	case IntentTell:
		if i.Name == "" {
			return fmt.Errorf("tell intent with empty target ghost")
		}
		if i.Text == "" {
			return fmt.Errorf("tell intent with empty text")
		}
	case IntentChat:
		if i.Text == "" {
			return fmt.Errorf("chat intent with empty text")
		}
	case IntentImagine:
		if i.Text == "" {
			return fmt.Errorf("imagine intent with empty prompt")
		}
	case IntentNote:
		if i.Text == "" {
			return fmt.Errorf("note intent with empty text")
		}
	case IntentSetExpression:
		if i.Name == "" {
			return fmt.Errorf("set_expression intent with empty pose")
		}
	case IntentPlayClip:
		if i.Name == "" {
			return fmt.Errorf("play_clip intent with empty clip name")
		}
	case IntentLook:
		// Text is optional: an empty look asks the driver's own question.
	case IntentHideSay, IntentMove, IntentLog:
	default:
		return fmt.Errorf("unknown intent type %q", i.Type)
	}
	return nil
}
