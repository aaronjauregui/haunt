package ghost

import (
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
	"github.com/aaronjauregui/haunt/internal/proto"
)

// Crashes, and what to say about them.
//
// A ghost runs on a good number of goroutines: the compositor's dispatch
// loop, an AI request or two, animation timers, the caret. A panic on any
// of them takes the whole process with it by default, which for a desktop
// companion means the ghost silently disappears mid-conversation. Worse,
// the report that reaches the user is a bare runtime error on a goroutine
// whose name means nothing to them.
//
// So the goroutines the driver starts run under guard: the panic is
// contained, the ghost carries on with one part of itself missing, and the
// log gets a report worth reading — what panicked, the stack, and the last
// few events the ghost received.
//
// The breadcrumbs are the part that earns its keep. A stack says where the
// program was; it does not say that the user had just dragged the ghost
// onto a second monitor, which is the sentence that turns a crash into a
// reproduction. `haunt run -record` keeps the same information in full, but
// nobody is recording when the thing they were not expecting happens.

// guard runs fn with panics contained and reported. Use it for any
// goroutine the driver starts: what is one failed animation frame with it
// is a dead ghost without it.
func (g *Ghost) guard(topic hlog.Topic, what string, fn func()) {
	defer g.recoverPanic(topic, what)
	fn()
}

// go_ starts fn on its own goroutine, guarded.
func (g *Ghost) go_(topic hlog.Topic, what string, fn func()) {
	go g.guard(topic, what, fn)
}

// recoverPanic is the deferred half of guard, for callers that need the
// defer themselves.
func (g *Ghost) recoverPanic(topic hlog.Topic, what string) {
	r := recover()
	if r == nil {
		return
	}
	g.log.Error(topic, "panic in %s: %v\n%s%s", what, r, g.breadcrumbs(), debug.Stack())
}

// crashReport formats a panic the way the log wants it, for callers that
// report through a channel rather than the log — the dispatch loop, whose
// panic ends the session and is reported as the session's error.
func (g *Ghost) crashReport(what string, r any, stack []byte) error {
	return fmt.Errorf("panic in %s: %v\n%s%s", what, r, g.breadcrumbs(), stack)
}

// breadcrumbs describes what the ghost was doing: the last intent it was
// given and the last few events it received.
func (g *Ghost) breadcrumbs() string {
	var b strings.Builder
	if in, ok := g.lastIntent(); ok {
		b.WriteString("  last intent: " + describeIntent(in) + "\n")
	}
	if g.bus != nil {
		recent := g.bus.Recent()
		if len(recent) > 0 {
			b.WriteString("  recent events:\n")
			for _, e := range recent {
				b.WriteString("    " + describeEvent(e) + "\n")
			}
		}
	}
	return b.String()
}

// intentLog remembers the last intent executed, so a crash can say what the
// brain had just asked for.
type intentLog struct {
	mu   sync.Mutex
	last proto.Intent
	at   time.Time
	set  bool
}

func (g *Ghost) noteIntent(in proto.Intent) {
	g.intents.mu.Lock()
	defer g.intents.mu.Unlock()
	g.intents.last, g.intents.at, g.intents.set = in, time.Now(), true
}

func (g *Ghost) lastIntent() (proto.Intent, bool) {
	g.intents.mu.Lock()
	defer g.intents.mu.Unlock()
	return g.intents.last, g.intents.set
}

// describeIntent is one line, and deliberately not the whole intent: an
// imagine prompt or a chat is the conversation, and a crash report ends up
// in bug threads.
func describeIntent(in proto.Intent) string {
	s := string(in.Type)
	if in.Name != "" {
		s += " name=" + in.Name
	}
	if in.Text != "" {
		s += fmt.Sprintf(" text=%d chars", len(in.Text))
	}
	return s
}

// describeEvent is the same bargain for events: type, timing and shape, not
// what was said.
func describeEvent(e proto.Event) string {
	s := fmt.Sprintf("#%d %s", e.Seq, e.Type)
	if e.Name != "" {
		s += " name=" + e.Name
	}
	if e.Region != "" {
		s += " region=" + e.Region
	}
	if e.X != 0 || e.Y != 0 {
		s += fmt.Sprintf(" at=(%d,%d)", e.X, e.Y)
	}
	if e.Text != "" {
		s += fmt.Sprintf(" text=%d chars", len(e.Text))
	}
	if !e.Time.IsZero() {
		s += " " + e.Time.Format("15:04:05.000")
	}
	return s
}
