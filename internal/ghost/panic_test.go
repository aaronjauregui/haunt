package ghost

import (
	"strings"
	"testing"

	"github.com/aaronjauregui/haunt/internal/bus"
	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/proto"
)

// A panic on one of the driver's goroutines must cost that goroutine, not
// the ghost: a companion that vanishes mid-conversation because an
// animation frame went wrong is worse than one missing an animation.
func TestGuardContainsAPanicAndSaysWhatHappened(t *testing.T) {
	rec, lines := hlog.Recorder()
	g := &Ghost{opts: Options{}, log: rec, bus: bus.New(8)}

	g.guard(hlog.Anim, "clip breathe", func() { panic("boom") })

	report := strings.Join(lines(), "\n")
	if !strings.Contains(report, "panic in clip breathe: boom") {
		t.Fatalf("the panic was not reported as one:\n%s", report)
	}
	if !strings.Contains(report, "panic_test.go") {
		t.Errorf("no stack in the report:\n%s", report)
	}
}

// The stack says where the program was; the breadcrumbs say what the ghost
// was being asked to do, which is the half that turns a crash into a
// reproduction.
func TestCrashReportCarriesWhatTheGhostWasDoing(t *testing.T) {
	rec, lines := hlog.Recorder()
	b := bus.New(8)
	g := &Ghost{opts: Options{}, log: rec, bus: b}

	// A gesture, a reaction, and the intent that was running when it went
	// wrong. The bus is unread here; these tests care about the record.
	b.Publish(proto.Event{Type: proto.EventClick, X: 40, Y: 12, Region: "head"})
	b.Publish(proto.Event{Type: proto.EventTimer, Name: "hush"})
	g.noteIntent(proto.Intent{Type: proto.IntentSay, Text: "hello there"})

	g.guard(hlog.Brain, "intent say", func() { panic("nope") })

	report := strings.Join(lines(), "\n")
	for _, want := range []string{
		"last intent: say",
		"recent events:",
		"#1 click",
		"region=head",
		"#2 timer name=hush",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("breadcrumbs missing %q:\n%s", want, report)
		}
	}

	// What was *said* stays out of it: a crash report is quoted in bug
	// threads, and dialogue is the user's conversation.
	if strings.Contains(report, "hello there") {
		t.Errorf("the crash report quoted the ghost's dialogue:\n%s", report)
	}
	if !strings.Contains(report, "11 chars") {
		t.Errorf("the report should still say how much text there was:\n%s", report)
	}
}

// The boot event carries the ghost's whole store; a crash report is not the
// place for it.
func TestBreadcrumbsDropTheBootSnapshot(t *testing.T) {
	b := bus.New(8)
	g := &Ghost{opts: Options{}, log: hlog.Discard(), bus: b}
	b.Publish(proto.Event{Type: proto.EventBoot, KV: map[string]string{"secret": "value"}})

	if got := g.breadcrumbs(); strings.Contains(got, "secret") {
		t.Errorf("the store leaked into the breadcrumbs:\n%s", got)
	}
	for _, e := range b.Recent() {
		if e.KV != nil {
			t.Error("the bus kept a KV snapshot in its ring")
		}
	}
}

// An intent that panics must not take the intent loop with it: the next one
// still runs.
func TestAPanickingIntentDoesNotStopTheNextOne(t *testing.T) {
	rec, lines := hlog.Recorder()
	g := &Ghost{opts: Options{}, log: rec, bus: bus.New(8)}

	// hide_say with no compositor and no shell: reaches stopTalking, which
	// dereferences the shell, which is nil here.
	g.execIntent(proto.Intent{Type: proto.IntentHideSay})
	// The loop survived, so this one is executed and remembered.
	g.execIntent(proto.Intent{Type: proto.IntentLog, Text: "still here"})

	if in, ok := g.lastIntent(); !ok || in.Type != proto.IntentLog {
		t.Errorf("last intent = %+v (%v), want the one after the panic", in, ok)
	}
	if !strings.Contains(strings.Join(lines(), "\n"), "still here") {
		t.Error("the intent after the panicking one never ran")
	}
}
