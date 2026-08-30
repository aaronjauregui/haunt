package troupe

import "time"

// Bringing a dead ghost back.
//
// The supervisor already contains a failure — one ghost's connection, bus,
// store and brain are its own, so its death is not the household's — but
// containment on its own left the dead one gone until the session
// restarted. That was defensible while the common cause of death was a bad
// manifest, which fails the same way however many times you try it. It
// stopped being defensible once a ghost could carry an errand: a companion
// that quietly died at noon is a meeting missed rather than a decoration
// absent, and nothing on the machine notices.
//
// The unit file cannot do this. `Restart=on-failure` in dist/haunt.service
// acts on the process, and `Run` blocks until every ghost has stopped: one
// death leaves a healthy process systemd has no reason to touch, and a
// policy that did fire would take the survivors down with the dead one.
// So it belongs here, one ghost at a time.
//
// Two rules shape it.
//
// **A ghost that died is restarted; a ghost that never lived is not.** The
// line is whether it ever got as far as being on screen. A failure on the
// way up — an unreadable manifest, a brain binary that is not there, a
// capability the shell cannot load — is a configuration failure, and it
// will reproduce itself identically five times in a row while the person
// reads five identical lines in the log. A failure after the ghost was
// running is the transient one: a model endpoint that went away, a
// compositor hiccup, a panic on a path nobody walks twice. Only the second
// kind is worth trying again.
//
// **Coming back has to cost something.** A ghost that crashes on boot
// after a life on screen would otherwise spin as fast as the machine
// allows, so each attempt waits longer than the last, and after enough of
// them the supervisor stops and says so. Staying up clears the count: the
// measure of recovery is that the ghost lived, not that it started.

// Restart is how a ghost that died on its own is brought back. The zero
// value is the policy described above with the defaults below, which is
// what an ordinary session wants; the fields exist so tests do not have to
// wait real seconds to watch a ghost give up.
type Restart struct {
	// Off leaves a dead ghost dead, which is what the supervisor did
	// before this existed.
	Off bool
	// Delay is the wait before the first restart, doubling with each
	// consecutive failure up to MaxDelay.
	Delay time.Duration
	// MaxDelay caps that doubling, so a ghost failing all afternoon is
	// still retried on a human timescale rather than once a fortnight.
	MaxDelay time.Duration
	// Tries is how many consecutive restarts are attempted before the
	// supervisor gives up and leaves the ghost stopped.
	Tries int
	// Steady is how long a life must last to count as a recovery. A ghost
	// that comes back and runs for this long has its failure count
	// cleared: the alternative is keying recovery off starting, which a
	// ghost that boots and dies every second does forever.
	Steady time.Duration
}

// Defaults for the zero value. Five tries over roughly half a minute is
// the same shape as the StartLimitBurst the unit file uses on the process,
// and for the same reason: a companion is not worth a boot loop.
const (
	defaultDelay    = 2 * time.Second
	defaultMaxDelay = 30 * time.Second
	defaultTries    = 5
	defaultSteady   = time.Minute
)

// settled fills in whatever the caller left at zero.
func (r Restart) settled() Restart {
	if r.Delay <= 0 {
		r.Delay = defaultDelay
	}
	if r.MaxDelay <= 0 {
		r.MaxDelay = defaultMaxDelay
	}
	// A cap below the first wait is not a cap, it is a shorter first wait.
	if r.MaxDelay < r.Delay {
		r.MaxDelay = r.Delay
	}
	if r.Tries <= 0 {
		r.Tries = defaultTries
	}
	if r.Steady <= 0 {
		r.Steady = defaultSteady
	}
	return r
}

// wait returns how long to pause before the nth consecutive restart, n
// counting from 1. Doubling, capped, and computed by shifting rather than
// multiplying in a loop so that a large n cannot overflow its way back
// round to no wait at all.
func (r Restart) wait(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	d := r.Delay
	for i := 1; i < n; i++ {
		if d >= r.MaxDelay {
			return r.MaxDelay
		}
		d *= 2
	}
	if d > r.MaxDelay {
		return r.MaxDelay
	}
	return d
}
