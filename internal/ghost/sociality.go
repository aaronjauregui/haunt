package ghost

import (
	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/proto"
)

// Ghosts watching each other.
//
// A ghost can already be *told* something by another ghost, which is
// address: somebody decided to speak to somebody. This is the other half.
// A ghost sees what its neighbours do in public — it spoke, it moved, it
// was poked, it drew something — and may react without being addressed at
// all. Two personas with different characters then develop dynamics nobody
// wrote, which is the same emergence the memory index gives one ghost,
// happening at the scale of a household.
//
// Three rules hold it together, and all three are inherited rather than
// invented here.
//
// **Public means visible.** Every announcement below sits after the
// checks that decide whether a thing actually reached the screen, not
// before them: a line suppressed by quiet hours was never said, and a
// ghost that "heard" it would be reading its neighbour's mind rather than
// watching it. What crosses the stage is an action and a string — see
// internal/troupe/stage.go for why the type is that narrow.
//
// **Observation is not address.** An `observed` event is not a prompted
// event (promptedEvent, curiosity.go), exactly as a `message` with From
// set is not. Nobody is waiting on the ghost, so reacting is unprompted by
// construction and pays for itself out of the curiosity allowance and the
// household ceiling above it. That is what stops two models talking each
// other into a chatter loop billed to the user, and it is why this needed
// no budget of its own.
//
// **Announcing is free; reacting is not.** Minting an observation costs a
// channel send, so it happens for every public action without any policy
// attached. Whether any of them is worth a reply is the receiving brain's
// decision and the driver's charge — the same split as the face at rest.

// announce publishes one public action to the other ghosts in the room.
// Safe on a ghost that is alone, which is the common case.
func (g *Ghost) announce(action, detail string) {
	if g.opts.Stage == nil {
		return
	}
	g.opts.Stage.Announce(action, detail)
}

// observed turns another ghost's public action into an event on this
// ghost's bus. It runs on the stage's goroutine, so it hands off and
// returns: the bus is the queue, and everything that thinks about the
// observation happens on the far side of it.
func (g *Ghost) observed(from, action, detail string) {
	g.log.Debug(hlog.Control, "observed: %s %s %s", from, action, detail)
	g.publish(proto.Event{
		Type: proto.EventObserved,
		From: from,
		Name: action,
		Text: detail,
	})
}
