package hermes

import (
	"context"
	"time"
)

// TurnRequest is one thing sent to the ghost's session.
type TurnRequest struct {
	// Input is the person's words verbatim, or an envelope.
	Input string
	// Context is the one line of ambient context layered on this turn as a
	// system message — what the ghost can see and the time of day — and
	// kept out of the persisted input so the session prefix stays stable.
	Context string
	// OnDelta receives the reply's text as it streams. Called from the
	// reading goroutine, in order.
	OnDelta func(delta string)
	// OnTool hears the model's tool calls starting and finishing, for the
	// why-trace and the working face. May be nil.
	OnTool func(ToolEvent)
}

// ToolEvent is one tool call seen in a turn's stream.
type ToolEvent struct {
	// Name is the tool as hermes names it.
	Name string
	// Done is false when the call starts and true when it has finished.
	Done bool
	// Failed is set on a finished call that hermes reported as an error.
	Failed bool
}

// Reply is what a turn came to.
type Reply struct {
	// Text is the full final reply, including the NO_REPLY token if that
	// is what it was: whether it is silence is the caller's decision to
	// record (see Silent).
	Text string
	// Tools is the tools called during the turn, in the order they started.
	Tools []string
}

// Turner carries turns to a ghost's brain. The ghost package holds one of
// these rather than a *Client so its tests can answer turns without a
// gateway.
type Turner interface {
	Turn(ctx context.Context, req TurnRequest) (Reply, error)
}

// Message is one entry of the session's record, for `haunt history`.
type Message struct {
	Role string
	Text string
	Time time.Time
	// Looked is what hermes itself did on the way to a reply, for `haunt
	// history -why`: its memory, session search and skill calls, with
	// what they returned. The ghost's own tools are left out; the driver
	// records those.
	Looked []Lookup
	// Interim is a reply written alongside tool calls, before the turn's
	// last word.
	Interim bool
}

// Lookup is one of hermes's own tool calls, and its result, both cut short.
type Lookup struct {
	Tool   string
	Args   string
	Result string
}
