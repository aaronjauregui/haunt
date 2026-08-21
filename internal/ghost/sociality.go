package ghost

import (
	"strings"

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
	g.noteNeighbour(from)
	g.publish(proto.Event{
		Type: proto.EventObserved,
		From: from,
		Name: action,
		Text: detail,
	})
}

// Who else is in the room, as far as this ghost can tell.
//
// It is not told: it learns the names by watching, which is the only way it
// finds out anything about its neighbours and is exactly the boundary the
// stage draws. The list exists for one small purpose — labelling what the
// ghost concludes about a neighbour as being about a *ghost* rather than a
// person or a project, when that conclusion goes into the entity index.
// Getting the label wrong would cost a word in a prompt, so this is
// deliberately the cheapest thing that could work rather than a roster.

// noteNeighbour remembers that this name belongs to another ghost.
func (g *Ghost) noteNeighbour(name string) {
	if name == "" {
		return
	}
	g.roomMu.Lock()
	defer g.roomMu.Unlock()
	if g.room == nil {
		g.room = map[string]bool{}
	}
	g.room[name] = true
}

// isNeighbour reports whether a name is one this ghost has watched act.
func (g *Ghost) isNeighbour(name string) bool {
	g.roomMu.Lock()
	defer g.roomMu.Unlock()
	return g.room[strings.TrimSpace(name)]
}

// subjectKind labels a subject for the entity index. Only one label is ever
// inferred here: everything else a ghost concludes about is a person, a
// project or a place, and deciding which is the consolidating model's job.
func (g *Ghost) subjectKind(name string) string {
	if g.isNeighbour(name) {
		return "ghost"
	}
	return ""
}
