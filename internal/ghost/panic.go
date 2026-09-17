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

// go_ starts fn on its own goroutine, guarded and counted. Counted because
// most of what goes through here ends in a write to the store — remembering
// an exchange, filing what the ghost concluded about a subject — and Run
// closes the store on its way out. Untracked, a Ctrl-C landing in the second
// after a reply meant the store went away underneath the goroutine still
// writing the memory of it, which surfaced as a "database is closed" warning
// and a conversation the ghost had no memory of having had.
//
// A goroutine started after teardown has begun is not run at all: there is
// nowhere left for its result to go, and saying so is better than letting it
// write into a closing store.
func (g *Ghost) go_(topic hlog.Topic, what string, fn func()) {
	if !g.bg.add() {
		g.log.Debug(topic, "not starting %s: the ghost is shutting down", what)
		return
	}
	go func() {
		defer g.bg.done()
		g.guard(topic, what, fn)
	}()
}

// backgroundGrace is how long teardown waits for that work. Long enough for
// a store write or a gateway to stop, short enough that a
// wedged backend does not hold the terminal of somebody who just pressed
// Ctrl-C. Whatever is still running when it expires is abandoned, which is
// where we were before counting them at all.
const backgroundGrace = 3 * time.Second

// waitForBackground lets the counted goroutines finish, up to the grace
// period, and refuses any that have not started yet.
func (g *Ghost) waitForBackground() {
	if done, left := g.bg.wait(backgroundGrace); !done {
		g.log.Debug(hlog.Session, "%d background %s still running at shutdown; abandoning",
			left, plural(left, "job", "jobs"))
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// background counts the goroutines go_ starts, so teardown can wait for
// them. A plain sync.WaitGroup is not enough on its own: Add on a
// zeroed group racing a Wait is a documented misuse, and the whole point
// here is that the two happen at once. The flag closes the door first, so
// no Add can begin once waiting has.
type background struct {
	mu      sync.Mutex
	wg      sync.WaitGroup
	running int
	closed  bool
}

func (b *background) add() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false
	}
	b.running++
	b.wg.Add(1)
	return true
}

func (b *background) done() {
	b.mu.Lock()
	b.running--
	b.mu.Unlock()
	b.wg.Done()
}

// wait closes the door and waits up to d, reporting whether everything
// finished and how many were still going if not.
func (b *background) wait(d time.Duration) (bool, int) {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()

	finished := make(chan struct{})
	go func() {
		b.wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
		return true, 0
	case <-time.After(d):
		b.mu.Lock()
		defer b.mu.Unlock()
		return false, b.running
	}
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
