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
	// EventCron is a manifest-scheduled trigger firing. Name is the
	// trigger's. Text is empty for an occasion — a morning greeting, an
	// anniversary — where the manifest names the moment and the brain finds
	// the words. A trigger that names a tool is a poll instead, and Text is
	// what the tool said: what a brain should do with that is take on an
	// intention, never speak, because a schedule silently skips the runs a
	// closed laptop was away for and an intention does not.
	EventCron   EventType = "cron"
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

	// EventSession reports the shape of the working day rather than a
	// single window: a long stretch, a flip-flop between two applications,
	// a title that keeps coming back. Requires the window capability, since
	// it is made from the same observations. See Event.Session.
	EventSession EventType = "session"

	// EventIntention is a standing intention coming due: Name identifies
	// it, Text is what the ghost meant to do. Unlike EventTimer this
	// survives a restart — the whole reason intentions exist — so it may
	// arrive for something set days ago, and it may arrive moments after
	// boot for something that fell due while the ghost was not running.
	EventIntention EventType = "intention"

	// EventActed answers an act intent: Name is the tool that ran
	// ("notes.append"), Text is what it said, and Tag echoes the intent's.
	// A tool that failed on its own terms — the file was not there, the
	// build is still running — arrives here too, in Text, because that is
	// an answer and the thing that asked should hear it. Only a refusal
	// (no capability, no consent, no allowance) comes back as EventAIError.
	EventActed EventType = "acted"

	// EventObserved is another resident ghost doing something in public:
	// it spoke, it moved, it was poked, it drew a picture. From names the
	// ghost, Name is the action (see the Acted constants) and Text is
	// whatever of it was visible.
	//
	// Observation is not address. Nobody is talking to this ghost and
	// nothing is waiting on it, so reacting is unprompted by construction
	// and charged like any other interruption — which is what stops two
	// personas talking each other into a chatter loop billed to the user.
	EventObserved EventType = "observed"
)

// The public actions a ghost can be observed doing. They name what a
// bystander in the room would have seen, which is the whole boundary:
// there is no action here for remembering something, for thinking, or for
// asking a model anything, because none of those are visible and a ghost
// must not become a side channel into another ghost's private state.
const (
	// ActedSpoke: a line appeared in its balloon. Text is the line.
	ActedSpoke = "spoke"
	// ActedMoved: it changed position on screen.
	ActedMoved = "moved"
	// ActedExpression: it changed its face. Text is the pose.
	ActedExpression = "expression"
	// ActedClip: it played an animation. Text is the clip's name.
	ActedClip = "clip"
	// ActedDrew: it painted a picture in its balloon. Text is what the
	// picture is of — visible in the ordinary sense that a picture of a
	// cat visibly contains a cat.
	ActedDrew = "drew"
	// ActedPoked: the user clicked on it.
	ActedPoked = "poked"
	// ActedUsedTool: it did something in the world through a tool server.
	// Text is the tool's name, not its arguments or its answer: what a
	// bystander saw was a ghost visibly working and a line in its balloon
	// about it, and the arguments are as private as the prompt behind a
	// picture.
	ActedUsedTool = "used_tool"
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
	// Text carries message payloads for EventMessage, and what a tool said
	// for a polling EventCron.
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
	// Poses lists the expressions the ghost's shell declares, included
	// only with EventBoot. A brain that means to choose a face — for a
	// line, or for an occasion offered by MayPose — needs to know which
	// faces exist, since the alternative is guessing names at a driver
	// that quietly ignores the ones the shell does not have.
	Poses []string `json:"poses,omitempty"`
	// MayPose is the driver offering the occasion: this is a moment the
	// ghost could reasonably change its face for, and the driver's own
	// rules about when a face may change at all are already satisfied.
	// Advisory — the brain decides whether to wear anything, and what.
	// A brain is free to set an expression without an offer; the offer is
	// about occasions nobody asked for, which is where the driver's rules
	// (nothing over a fullscreen window, nothing under reduced motion)
	// apply.
	MayPose bool `json:"may_pose,omitempty"`
	// Tools lists the tools the ghost may act through, included only with
	// EventBoot, in "server.tool" form. Present for the same reason Poses
	// is: a brain that means to do something needs to know what there is
	// to do, and the alternative is guessing names at a driver that
	// refuses the ones that do not exist. Empty when the ghost declares no
	// tool servers, or when the tools capability is not active — a brain
	// is told what it may use, not what it might have used.
	Tools []string `json:"tools,omitempty"`
	// Session carries what was noticed about the working day, for
	// EventSession. Summary is the fact in a sentence, which is what a
	// brain hands to a model; the rest is there for one that would rather
	// decide for itself.
	Session *SessionFact `json:"session,omitempty"`
}

// SessionFact is something noticed about how the day is going: see
// internal/session, which computes them, and EventSession, which delivers
// them.
type SessionFact struct {
	// Kind is "dwell", "alternation" or "recurrence".
	Kind string `json:"kind"`
	// App is the application, or both of them for an alternation.
	App string `json:"app,omitempty"`
	// Title is the window title, when the fact is about one.
	Title string `json:"title,omitempty"`
	// Minutes is how long (dwell); Count is how many (alternation,
	// recurrence).
	Minutes int `json:"minutes,omitempty"`
	Count   int `json:"count,omitempty"`
	// Summary is the fact in a sentence: "42 minutes in nvim (session.go)".
	Summary string `json:"summary,omitempty"`
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
	IntentWatch         IntentType = "watch"          // the user asked for window awareness; ask and start it
	IntentAct           IntentType = "act"            // run one tool on a declared server
	IntentIntend        IntentType = "intend"         // hold a purpose, across restarts
	IntentResolve       IntentType = "resolve"        // that purpose is discharged
	IntentSetAside      IntentType = "set_aside"      // stop watching a goal, without pretending it was done
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
	// DelayMS is the timer delay for IntentSetTimer, and when an
	// IntentIntend falls due. Zero on an intention means it has no moment —
	// it is simply carried until it is resolved.
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
	// LingerMS hides what this intent puts on screen after the given delay,
	// and only if it is still the thing on screen. It is the fix for a bug
	// every brain would otherwise write: a timer set to hide *this* line
	// fires later and hides whatever arrived meanwhile — which, when a
	// model is slow, is the reply the person was waiting for.
	//
	// The driver can get this right because it knows what is on the screen
	// and the brain does not. Zero means the line stays until something
	// replaces it.
	LingerMS int `json:"linger_ms,omitempty"`
	// About names the subject an utterance concerns, so that what the ghost
	// concludes is filed under it in the entity index rather than only
	// dissolving into the memory pool. It is how a ghost comes to know who
	// its neighbour *is* rather than merely reacting to it each time: a
	// remark made about another resident ghost becomes one of the handful
	// of things this ghost would say about it if asked.
	//
	// The brain names the subject because the brain is the thing that knows
	// what it is talking about; the driver only files it. Ignored on an
	// intent that produces no words.
	About string `json:"about,omitempty"`
	// Args is the JSON arguments object for IntentAct, passed to the tool
	// server unchanged. The driver does not validate it against the tool's
	// schema: that contract is the server's, it will reject a bad call
	// itself, and a driver re-implementing somebody else's schema badly is
	// a driver that refuses calls that would have worked.
	Args json.RawMessage `json:"args,omitempty"`
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
	// watch carries nothing: it is a request to put a question, and every
	// term of that question is the driver's.
	case IntentWatch:
	case IntentIntend:
		if i.Name == "" {
			return fmt.Errorf("intend intent with empty name")
		}
		if i.Text == "" {
			return fmt.Errorf("intend intent with nothing to intend")
		}
		if i.DelayMS < 0 {
			return fmt.Errorf("intend intent with a negative delay %d", i.DelayMS)
		}
	case IntentResolve:
		if i.Name == "" {
			return fmt.Errorf("resolve intent with empty name")
		}
	// Setting a goal aside is not resolving it. A goal the person has given
	// up on was never discharged, and a driver that quietly deleted it
	// would be writing a lie into the journal; what changes is only that it
	// stops being watched and stops being brought up.
	case IntentSetAside:
		if i.Name == "" {
			return fmt.Errorf("set_aside intent with empty name")
		}
	case IntentAct:
		if err := ValidToolName(i.Name); err != nil {
			return fmt.Errorf("act intent: %w", err)
		}
		// An argument list that is not a JSON object is a call no server
		// can read, and the failure is much clearer here than three
		// processes away.
		if len(i.Args) > 0 && !json.Valid(i.Args) {
			return fmt.Errorf("act intent for %s: args are not valid JSON", i.Name)
		}
	case IntentHideSay, IntentMove, IntentLog:
	default:
		return fmt.Errorf("unknown intent type %q", i.Type)
	}
	return nil
}

// ValidToolName checks the "server.tool" form every tool is named in. It is
// here rather than in the driver because it is part of the schema: a brain
// building the name from two strings should be able to check it with the
// same rule the driver will apply.
func ValidToolName(name string) error {
	server, tool, ok := strings.Cut(name, ".")
	if !ok {
		return fmt.Errorf("tool name %q is not in server.tool form", name)
	}
	if server == "" || tool == "" {
		return fmt.Errorf("tool name %q has an empty half", name)
	}
	if strings.Contains(tool, ".") {
		return fmt.Errorf("tool name %q has more than one dot", name)
	}
	return nil
}
