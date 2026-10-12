package ghost

import (
	"strings"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/model/fake"
	"github.com/aaronjauregui/haunt/internal/proto"
)

// exchangeAt writes one finished turn to the transcript as if it had
// happened at at: the person's words and the ghost's reply.
func exchangeAt(t *testing.T, b *modelBrain, at time.Time) {
	t.Helper()
	turn, err := b.tr.nextTurn()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []TranscriptRow{
		{Turn: turn, Kind: rowInput, Text: "hello", At: at},
		{Turn: turn, Kind: rowWords, Text: "Hello.", At: at},
	} {
		if err := b.tr.add(r); err != nil {
			t.Fatal(err)
		}
	}
}

// The second line of a turn's context says how long since the last
// exchange, in the units a person would use, and is left out before the
// first.
func TestLastExchangeLine(t *testing.T) {
	g, b, _ := modelGhost(t, 0)
	sunday := time.Date(2026, 10, 11, 20, 0, 0, 0, time.Local)
	if ctx := g.turnContext(sunday); strings.Contains(ctx, "Last exchange") {
		t.Errorf("a line before any exchange: %q", ctx)
	}
	for _, c := range []struct {
		at, now time.Time
		want    string
	}{
		{sunday.Add(-72 * time.Hour), sunday, "Last exchange: 3 days ago (Thursday evening)."},
		// Calendar days: Thursday evening is three days before Sunday
		// morning, though it is not yet 72 hours.
		{sunday.Add(-72 * time.Hour), sunday.Add(-12 * time.Hour), "Last exchange: 3 days ago (Thursday evening)."},
		{sunday.Add(-11 * time.Hour), sunday.Add(-6 * time.Hour), "Last exchange: 5 hours ago (this morning)."},
		{sunday.Add(-23 * time.Hour), sunday.Add(-6 * time.Hour), "Last exchange: 17 hours ago (yesterday evening)."},
		{sunday.Add(-20 * time.Minute), sunday, "Last exchange: 20 minutes ago."},
	} {
		exchangeAt(t, b, c.at)
		lines := strings.Split(g.turnContext(c.now), "\n")
		if len(lines) < 2 || !strings.HasPrefix(lines[0], "Now: ") || lines[1] != c.want {
			t.Errorf("context %q, want the second line %q", lines, c.want)
		}
	}
	// An event is not an exchange.
	b.tr.add(TranscriptRow{Turn: 99, Kind: rowEvent, Text: Envelope("idle"), At: sunday})
	if lines := strings.Split(g.turnContext(sunday), "\n"); lines[1] != "Last exchange: 20 minutes ago." {
		t.Errorf("an event moved the last exchange: %q", lines[1])
	}
}

// A ghost started after a long gap carries the gap on its boot envelope
// and gets no returned turn, then or later for the same gap: one greeting
// after a restart. A second boot the same minute carries none.
func TestReturnedEnvelopeOnBoot(t *testing.T) {
	g, b, srv := modelGhost(t, 5, silent("kind=boot"), silent("kind=returned"), silent(""))
	g.awayAfter = 8 * time.Hour
	g.store.Set(metKey, "yes")
	exchangeAt(t, b, time.Now().Add(-72*time.Hour))
	b.Deliver(proto.Event{Type: proto.EventBoot})
	waitTurns(t, b, 2)
	got := sent(t, srv)
	if len(got) != 1 || !strings.HasPrefix(got[0], "[haunt event] kind=boot first_time=false since=3d last=") {
		t.Fatalf("sent %q, want the boot turn carrying the gap, alone", got)
	}
	// The person goes idle and comes back: the gap was told already.
	g.endResponding()
	b.Deliver(proto.Event{Type: proto.EventIdle})
	waitTurns(t, b, 3)
	g.endResponding()
	b.Deliver(proto.Event{Type: proto.EventSession, Session: &proto.SessionFact{Kind: "returning", Minutes: 600}})
	waitTurns(t, b, 4)
	g.endResponding()
	b.Deliver(proto.Event{Type: proto.EventBoot})
	waitTurns(t, b, 5)
	got = sent(t, srv)
	for _, in := range got {
		if EnvelopeKind(in) == "returned" {
			t.Errorf("a returned turn for a gap the boot told: %q", got)
		}
	}
	if last := got[len(got)-1]; last != "[haunt event] kind=boot first_time=false" {
		t.Errorf("the second boot: %q", last)
	}
}

// A returned turn is charged to initiative like any unprompted one, and
// refunded when it ends in silence. Sent on the first session fact after
// the gap, it stands in for that fact.
func TestReturnedCharged(t *testing.T) {
	g, b, srv := modelGhost(t, 5,
		fake.Rule{Match: "kind=returned", Calls: []fake.Call{{Name: silentName}}, DelayMS: 300},
		silent(""))
	g.awayAfter = 8 * time.Hour
	exchangeAt(t, b, time.Now().Add(-10*time.Hour))
	b.Deliver(proto.Event{Type: proto.EventIdle})
	waitTurns(t, b, 1)
	g.endResponding()
	b.Deliver(proto.Event{Type: proto.EventSession, Session: &proto.SessionFact{Kind: "returning", Minutes: 600}})
	waitBusy(t, b)
	if n := spent(g.curiosity); n != 1 {
		t.Errorf("%d wakes charged during the returned turn, want 1", n)
	}
	waitTurns(t, b, 2)
	if n := spent(g.curiosity); n != 0 {
		t.Errorf("a silent returned turn stayed charged: %d", n)
	}
	got := sent(t, srv)
	if len(got) != 2 || !strings.HasPrefix(got[1], "[haunt event] kind=returned since=10h last=") {
		t.Errorf("sent %q, want the idle turn then the returned one in place of the session fact", got)
	}
}

// Below the threshold, with the threshold off, or past it with the person
// never having gone idle, nothing is sent for the gap; the session fact
// goes as it would.
func TestReturnedBelowThreshold(t *testing.T) {
	g, b, srv := modelGhost(t, 5, silent(""))
	g.awayAfter = 8 * time.Hour
	g.store.Set(metKey, "yes")
	exchangeAt(t, b, time.Now().Add(-7*time.Hour))
	b.Deliver(proto.Event{Type: proto.EventBoot})
	waitTurns(t, b, 1)
	g.endResponding()
	b.Deliver(proto.Event{Type: proto.EventIdle})
	waitTurns(t, b, 2)
	g.endResponding()
	b.Deliver(proto.Event{Type: proto.EventSession, Session: &proto.SessionFact{Kind: "returning", Minutes: 30}})
	waitTurns(t, b, 3)
	for _, in := range sent(t, srv) {
		if EnvelopeKind(in) == "returned" {
			t.Errorf("a returned turn below the threshold: %q", in)
		}
	}

	// Past the threshold but with nobody having gone idle since: a long
	// stretch of work, not a return.
	g2, b2, srv2 := modelGhost(t, 5, silent(""))
	g2.awayAfter = 8 * time.Hour
	exchangeAt(t, b2, time.Now().Add(-9*time.Hour))
	b2.Deliver(proto.Event{Type: proto.EventWindow, Name: "foot", Text: "~"})
	time.Sleep(50 * time.Millisecond)
	if got := sent(t, srv2); len(got) != 0 {
		t.Errorf("sent %q on a window change with no idle since the last exchange", got)
	}

	// Off: away_hours = 0.
	g3, b3, srv3 := modelGhost(t, 5, silent(""))
	g3.store.Set(metKey, "yes")
	exchangeAt(t, b3, time.Now().Add(-72*time.Hour))
	b3.Deliver(proto.Event{Type: proto.EventBoot})
	waitTurns(t, b3, 1)
	time.Sleep(50 * time.Millisecond)
	if got := sent(t, srv3); len(got) != 1 {
		t.Errorf("sent %q with away_hours off, want the boot turn alone", got)
	}
}
