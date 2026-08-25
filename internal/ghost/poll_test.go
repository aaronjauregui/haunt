package ghost

import (
	"strings"
	"testing"

	"github.com/aaronjauregui/haunt/internal/mcp"
	"github.com/aaronjauregui/haunt/internal/proto"
)

// pollingGhost is an acting ghost with one scheduled trigger on it, and the
// tool behind that trigger already consented to — which is the state a
// ghost is in every time after the first.
func pollingGhost(t *testing.T) (*Ghost, *fakeServer, *recorder) {
	t.Helper()
	g, srv, r := actingGhost(t)
	srv.res = mcp.Result{Text: "standup at 15:00"}
	r.record("tool:notes.append", true)
	return g, srv, r
}

// calendarTrigger is the manifest line the whole direction is about.
func calendarTrigger() TriggerSection {
	return TriggerSection{
		Cron: "*/15 * * * *",
		Name: "calendar",
		Tool: "notes.append",
		Args: map[string]any{"within": "2h"},
	}
}

// The primitive: the driver calls the tool on schedule and what it said
// reaches the brain, with the trigger's own name on it.
func TestAPollDeliversWhatTheToolSaid(t *testing.T) {
	g, srv, _ := pollingGhost(t)

	g.pollTrigger(calendarTrigger())

	if got := srv.ran(); len(got) != 1 || got[0] != `append({"within":"2h"})` {
		t.Fatalf("ran %v, want the one call with the manifest's arguments", got)
	}
	var polled []proto.Event
	for _, e := range drain(g.bus) {
		if e.Type == proto.EventCron {
			polled = append(polled, e)
		}
	}
	if len(polled) != 1 {
		t.Fatalf("%d cron events, want the one for the poll", len(polled))
	}
	if polled[0].Name != "calendar" || polled[0].Text != "standup at 15:00" {
		t.Errorf("event = %+v, want the trigger's name and the tool's answer", polled[0])
	}
}

// A poll is reading, not acting, and it leaves none of acting's three
// marks. The journal one matters most: entries ride into every prompt, so a
// calendar polled every quarter of an hour would slowly replace what the
// ghost concluded with what it read.
func TestAPollIsNotWrittenDown(t *testing.T) {
	g, _, _ := pollingGhost(t)

	g.pollTrigger(calendarTrigger())

	if notes := g.loadNotes(); len(notes) != 0 {
		t.Errorf("journal has %d entries, want a poll to leave none: %+v", len(notes), notes)
	}
	for _, e := range drain(g.bus) {
		if e.Type == proto.EventObserved {
			t.Errorf("the room was told about a poll: %+v", e)
		}
	}
}

// Finding the same thing again costs nothing. A calendar that has been
// saying "standup at 15:00" all morning has already been decided about, and
// re-delivering it would have the brain deciding again on every poll.
func TestAnUnchangedAnswerIsNotDeliveredTwice(t *testing.T) {
	g, srv, _ := pollingGhost(t)

	g.pollTrigger(calendarTrigger())
	drain(g.bus)
	g.pollTrigger(calendarTrigger())
	if events := cronEvents(drain(g.bus)); len(events) != 0 {
		t.Errorf("the unchanged answer was delivered again: %+v", events)
	}

	// And a change is: this is what the ghost is there for.
	srv.res = mcp.Result{Text: "standup moved to 15:30"}
	g.pollTrigger(calendarTrigger())
	if events := cronEvents(drain(g.bus)); len(events) != 1 {
		t.Errorf("%d events for a changed answer, want one", len(events))
	}
	if len(srv.ran()) != 3 {
		t.Errorf("ran %v, want every poll to have asked", srv.ran())
	}
}

// Nothing upcoming is the ordinary answer for most of a day, and it is not
// something to hand a character to talk about.
func TestAnEmptyAnswerIsNotDelivered(t *testing.T) {
	g, srv, _ := pollingGhost(t)
	srv.res = mcp.Result{}

	g.pollTrigger(calendarTrigger())

	if events := cronEvents(drain(g.bus)); len(events) != 0 {
		t.Fatalf("an empty answer reached the brain: %+v", events)
	}
	// And it is remembered as nothing, so a calendar that empties out and
	// fills up again with the same appointment is news both times.
	srv.res = mcp.Result{Text: "standup at 15:00"}
	g.pollTrigger(calendarTrigger())
	srv.res = mcp.Result{}
	g.pollTrigger(calendarTrigger())
	drain(g.bus)
	srv.res = mcp.Result{Text: "standup at 15:00"}
	g.pollTrigger(calendarTrigger())
	if events := cronEvents(drain(g.bus)); len(events) != 1 {
		t.Errorf("%d events for the appointment coming back, want one", len(events))
	}
}

// A poll that lands while nobody is at the desk asks nothing and does
// nothing. Consent is asked in the moment, and a permission question
// arriving out of a background loop into an empty room is the pattern the
// whole consent design exists to replace.
func TestAPollDoesNotAskAnEmptyRoom(t *testing.T) {
	g, srv, _ := actingGhost(t) // nothing decided about the tool yet
	g.idle.Store(true)

	g.pollTrigger(calendarTrigger())

	if len(srv.ran()) != 0 {
		t.Errorf("ran %v, want nothing while there was nobody to ask", srv.ran())
	}
	if g.choiceOpen() {
		t.Error("a permission question was put to an empty room")
	}
}

// And when they are there, the question says what it is really about: not
// this call, but a standing arrangement.
func TestAPollAsksAboutTheSchedule(t *testing.T) {
	g, srv, _ := actingGhost(t)
	asked := answerAsking(t, g, consentAllow)

	g.pollTrigger(calendarTrigger())

	question := asked()
	if !strings.Contains(question, "notes.append") {
		t.Errorf("question = %q, want the tool named in it", question)
	}
	if !strings.Contains(question, "on a schedule") {
		t.Errorf("question = %q, want it to say the call is a scheduled one", question)
	}
	if len(srv.ran()) != 1 {
		t.Errorf("ran %v, want the poll to have gone ahead once allowed", srv.ran())
	}
}

// A refused tool is refused on a schedule too, silently and for good.
func TestARefusedToolIsNotPolled(t *testing.T) {
	g, srv, r := actingGhost(t)
	r.record("tool:notes.append", false)

	g.pollTrigger(calendarTrigger())

	if len(srv.ran()) != 0 {
		t.Errorf("ran %v, want nothing after a refusal", srv.ran())
	}
}

// The rule that makes the direction reliable: what a poll produces is
// something the ghost is *meaning to do*, and because the trigger is on the
// ghost's books rather than being its own idea, raising it later spends
// from the obligation allowance rather than competing with small talk.
func TestAnIntentionTakenOnAfterAPollIsAnObligation(t *testing.T) {
	g, _, _ := pollingGhost(t)

	g.pollTrigger(calendarTrigger())
	g.execIntend(proto.Intent{
		Type: proto.IntentIntend, Name: "meeting",
		Text: "tell them about the standup", DelayMS: 60000,
	})

	list := g.loadIntentions()
	if len(list) != 1 {
		t.Fatalf("%d intentions, want the one the poll led to", len(list))
	}
	if !list[0].Asked {
		t.Error("an intention taken on off the back of a manifest trigger was charged to the ghost's whims")
	}
}

// The inversion worth having: a house that knows you left is a better
// answer to "should the ghost be talking" than 22:00-08:00 is.
func TestAPresencePollKeepsTheGhostQuiet(t *testing.T) {
	g, srv, _ := pollingGhost(t)
	tr := calendarTrigger()
	tr.Name, tr.QuietWhen = "presence", "not_home"
	srv.res = mcp.Result{Text: "state: not_home"}

	if muted, _ := g.quiet.Active(); muted {
		t.Fatal("the ghost was quiet before anything said so")
	}
	g.pollTrigger(tr)

	muted, why := g.quiet.Active()
	if !muted {
		t.Fatal("nobody is home and the ghost is still talking")
	}
	if !strings.Contains(why, "home") {
		t.Errorf("reason = %q, want it to say why", why)
	}
	// And it feeds quiet and nothing else: a presence poll that also
	// produced remarks would be the ghost narrating the front door.
	if events := cronEvents(drain(g.bus)); len(events) != 0 {
		t.Errorf("a presence poll reached the brain: %+v", events)
	}

	// Coming home is believed at once, unlike leaving, which expires.
	srv.res = mcp.Result{Text: "state: home"}
	g.pollTrigger(tr)
	if muted, why := g.quiet.Active(); muted {
		t.Errorf("still quiet after coming home (%s)", why)
	}
}

func cronEvents(events []proto.Event) []proto.Event {
	var out []proto.Event
	for _, e := range events {
		if e.Type == proto.EventCron {
			out = append(out, e)
		}
	}
	return out
}
