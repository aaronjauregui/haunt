package ghost

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/proto"
)

// The world outside the desktop.
//
// A ghost could already act on the world: `[[tool]]` declares an MCP
// server, the `tools` capability says it may act at all, each tool is
// consented to by name, and the journal keeps what was done. What it could
// not do was be *woken* by any of it. MCP is pull-only — a tool reaches the
// model during an exchange somebody started — and the three inbound paths
// were the compositor, the control socket, and a cron trigger carrying a
// name and nothing else. Nothing in the tree could express "it is a quarter
// to three and there is a standup at three".
//
// The missing piece is one field: a scheduled tool call whose result
// reaches the brain.
//
//	[[trigger]]
//	cron = "*/15 * * * *"
//	name = "calendar"
//	tool = "calendar.upcoming"
//	args = { within = "2h" }
//
// The consequence worth having is that **haunt ships no integrations at
// all.** Home Assistant is an MCP server plus one trigger line. Nextcloud
// is an MCP server plus one trigger line. Whatever you use next year is an
// MCP server plus one trigger line. That is the same bet already made on
// one wire format for models, and it applies with more force here, because
// there are fifty services somebody might want and not one of them is
// interesting enough to justify code in a driver whose subject is a
// character on a screen.
//
// # A poll creates an intention; it does not speak
//
// This is the rule that makes the direction reliable, and it falls out of a
// property of cron that is easy to miss: **both crontab and an in-process
// scheduler silently skip missed runs.** Laptop closed at 14:55, opened at
// 15:05, and a 15:00 trigger simply did not happen, with nothing anywhere
// knowing it did not.
//
// So a poll must never be the thing that remembers. When a calendar poll
// finds a meeting in ten minutes, what should happen is that the brain
// takes on an *intention* for it — then it survives a restart between the
// poll and the meeting, it is still owed if it comes due during a
// fullscreen game, and it is discharged deliberately rather than by having
// fired, which is the whole reason intention.go exists. That splits the two
// cleanly, and the split is the design: **cron triggers are for recurring
// occasions that are fine to miss** (say good morning; poll the calendar),
// and **intentions are for obligations that must not be**. The polling
// interval is then a tuning decision rather than a reliability one.
//
// The driver cannot force a brain to do that, and does not try. What it
// does is make it the easy thing: the poll's answer arrives as an ordinary
// event with no window opened for speaking on its own account, and an
// intention taken on in the moments after it inherits the obligation the
// trigger carries (see markObliged, and Asked in execIntend). The hermes
// brain does exactly this and nothing else — see the EventCron case in
// hermesbrain.go's Deliver.
//
// # Never the source of truth
//
// A rule for the whole of this: the ghost **surfaces what an authority
// already knows and never becomes the authority**. No local copy of your
// schedule that can drift, no ghost-owned task store shadowing somebody
// else's, and never the only thing that would have reminded you. It is
// worth stating as a rule rather than leaving to judgement because every
// violation of it is individually reasonable — caching the day's events so
// the ghost can talk about them offline is obviously sensible — and
// collectively they turn a companion into a badly-written calendar client
// with a synchronisation bug.
//
// Two things follow, and both are enforced here rather than hoped for.
// Nothing polled is handed to the model as something to remember: what may
// be remembered is a *conclusion* — they have moved that session three
// weeks running — and never what was read, or a year of "meeting with Bob"
// buries everything human about the person. And nothing polled is
// journalled, for the same reason.
//
// # What a poll costs when it finds nothing
//
// Nothing, which is the property the goal check has for the same reason. An
// answer identical to the last one is not delivered at all: a calendar that
// has been saying "standup at 15:00" for the past hour has already been
// decided about, and re-delivering it would have the brain deciding again
// every quarter of an hour, on the user's bill. Only a *change* is worth
// waking anybody for — and an empty answer, which is what most of a day
// looks like, is not delivered at all.

const (
	// awayGrace is how long a presence poll's "nobody is home" stands
	// before it expires on its own. It has to outlast the poll interval
	// that set it and it must not outlast the situation: a laptop that
	// stops polling — asleep, off the network, the server gone — would
	// otherwise leave a ghost mute indefinitely on the strength of one
	// reading half an hour old. Quiet that decays back to talking is the
	// failure that gets noticed; quiet that never lifts is the one that
	// gets uninstalled.
	awayGrace = 30 * time.Minute
	// pollAnswerChars is how much of an answer is carried to the brain. A
	// tool server asked for the next two hours of a calendar may reply with
	// a page of JSON, and what rides into a prompt has to be a line or two.
	pollAnswerChars = 400
)

// polls remembers what each trigger last said, so an unchanged answer is
// not delivered twice. In memory and not in the store, deliberately: it is
// about what the brain has already been told this session, and a ghost that
// has just started has told it nothing.
type polls struct {
	mu   sync.Mutex
	last map[string]string
}

// changed reports whether this trigger's answer is new, and remembers it.
func (p *polls) changed(name, answer string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last == nil {
		p.last = map[string]string{}
	}
	if p.last[name] == answer {
		return false
	}
	p.last[name] = answer
	return true
}

// pollTrigger is one scheduled tool call, from the top: is the ghost
// allowed to act at all, is this tool allowed, ask the server, and decide
// what — if anything — the brain hears about it.
//
// Everything it can decline to do, it declines silently. A poll is
// machinery running on its own schedule with nobody watching, and the one
// thing it must never do is turn a missing permission or a broken server
// into something on screen.
func (g *Ghost) pollTrigger(tr TriggerSection) {
	ref, err := parseToolRef(tr.Tool)
	if err != nil {
		// Settled at load; here so a manifest reaching this by another
		// route cannot make the driver call something it cannot name.
		g.log.Warn(hlog.Tool, "trigger %s: %v", tr.Name, err)
		return
	}
	if !g.capabilityActive("tools") {
		g.log.Debug(hlog.Tool, "trigger %s: the tools capability is not granted", tr.Name)
		return
	}
	// Granted possibly since boot, in which case the servers have never
	// been started. Cheap and idempotent when they have.
	g.startToolServers()
	if _, known := g.lookupTool(ref); !known {
		g.log.Debug(hlog.Tool, "trigger %s: %s is not on offer", tr.Name, ref)
		return
	}
	// Consent for a scheduled call is asked in the moment like any other,
	// and the moment has to be one the person is in. A question about a
	// poll is the one shape consent.go rules out — a permission popup
	// arriving from a background loop, about nothing that just happened —
	// so it is put only while somebody is at the desk, where it is a
	// question about a thing their ghost is going to keep doing. If they
	// are not, this poll is skipped and the next one asks.
	if !g.toolGranted(ref) && g.idle.Load() {
		g.log.Debug(hlog.Tool, "trigger %s: %s is not consented to and nobody is there to ask", tr.Name, ref)
		return
	}
	args, err := json.Marshal(tr.Args)
	if err != nil {
		g.log.Warn(hlog.Tool, "trigger %s: %v", tr.Name, err)
		return
	}
	if len(tr.Args) == 0 {
		args = nil
	}
	out := g.callTool(toolCall{ref: ref, args: args, scheduled: true})
	switch {
	case out.Refused:
		g.log.Debug(hlog.Tool, "trigger %s: %s", tr.Name, out.Text)
		return
	case out.Failed:
		// A poll that failed is a poll that did not happen, and the next
		// one is fifteen minutes away. Nothing is told to the brain,
		// because "the calendar server is unwell" is a thing for a log and
		// not a thing for a character to bring up.
		g.log.Warn(hlog.Tool, "trigger %s: %s", tr.Name, out.Text)
		return
	}
	answer := clip(out.Text, pollAnswerChars)
	// Presence first, and on every poll rather than only on a change: what
	// is being kept up to date is a deadline, and a reading that says the
	// same thing as the last one is exactly what should push it forward.
	if tr.QuietWhen != "" {
		g.notePresence(tr, answer)
		return
	}
	// Remembered even when it is nothing, so that a calendar which empties
	// out and fills up again with the same appointment is a change both
	// times rather than only the first.
	changed := g.polls.changed(tr.Name, answer)
	if answer == "" {
		// Nothing upcoming is the ordinary answer for most of a day, and
		// it is not something to hand a character to talk about.
		g.log.Debug(hlog.Tool, "trigger %s: nothing to report", tr.Name)
		return
	}
	if !changed {
		g.log.Debug(hlog.Tool, "trigger %s: nothing has changed", tr.Name)
		return
	}
	g.log.Info(hlog.Tool, "trigger %s: %s", tr.Name, answer)
	// The occasion is the author's and the person accepted it when they
	// installed the ghost, so anything the ghost takes on off the back of
	// it is on its books rather than being a whim — which is what stops a
	// reminder about your three o'clock competing with small talk. It is
	// not a licence to speak: an utterance charged here is charged against
	// the obligation allowance and rationed like everything else.
	g.markObliged()
	g.publish(proto.Event{Type: proto.EventCron, Name: tr.Name, Text: answer})
}

// notePresence is the inversion worth having: quiet hours are clock-driven
// and shush is manual, and a house that knows you have left is a far better
// answer to "should the ghost be talking" than 22:00-08:00 is.
//
// The driver knows no vendor's vocabulary for it and never will. The
// manifest carries the word to look for (`quiet_when`), the answer is
// searched for it, and the whole of the driver's understanding of presence
// is a deadline. It is per-ghost rather than shared with the room, because
// the trigger is in this ghost's manifest and the tool behind it was
// consented to for this ghost — a ghost that silenced its neighbours on the
// strength of a permission they were never asked about would be a hole, not
// a convenience.
func (g *Ghost) notePresence(tr TriggerSection, answer string) {
	away := strings.Contains(strings.ToLower(answer), strings.ToLower(tr.QuietWhen))
	if !away {
		if g.quiet.Home() {
			g.log.Info(hlog.Session, "trigger %s: somebody is home again", tr.Name)
		}
		return
	}
	if g.quiet.Away(time.Now().Add(awayGrace)) {
		g.log.Info(hlog.Session, "trigger %s: nobody is home, so keeping quiet", tr.Name)
	}
}
