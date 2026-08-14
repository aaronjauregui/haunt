package ghost

import (
	"fmt"
	"hash/fnv"
	"math/rand"
	"sync"
	"time"

	"github.com/aaronjauregui/haunt/internal/shell"
)

// Animation. Clips swap layer variants on a clock — a blink is two
// keyframes 90ms apart — and the driver runs them by scheduling a wake-up
// per keyframe rather than by running a frame loop. A blinking ghost costs
// two timers and two repaints, not thirty repaints a second, which is what
// makes idle animation affordable on a CPU compositor.
//
// Several clips may run at once, because they usually drive different
// layers: blinking while the mouth talks. When two clips reach for the same
// layer, the one started most recently wins, and the pose shows through
// wherever no clip has an opinion.

// Random intervals for the automatic triggers. Wide enough that two ghosts
// on one screen do not blink in lockstep.
var triggerDelays = map[string][2]time.Duration{
	shell.TriggerSometimes: {3 * time.Second, 12 * time.Second},
	shell.TriggerRarely:    {45 * time.Second, 150 * time.Second},
}

// running is one clip in flight.
type running struct {
	clip    shell.Clip
	started time.Time
	// order records when it started, so the newest clip wins a layer.
	order int
	timer *time.Timer
}

type animator struct {
	mu      sync.Mutex
	active  map[string]*running
	pending map[string]*time.Timer // scheduled starts for automatic clips
	seq     int
	rng     *rand.Rand
	stopped bool
}

// startAnimation begins the shell's automatic clips. Reduced motion stops
// the automatic ones only: a clip the brain asks for is saying something,
// and a ghost that ignores its own brain is broken rather than considerate.
func (g *Ghost) startAnimation() {
	if g.shell == nil || len(g.shell.Clips()) == 0 {
		return
	}
	// Seeded from the ghost's name: idle animation should be unpredictable
	// to a person and identical across replays of a recorded session.
	h := fnv.New64a()
	h.Write([]byte(g.manifest.Ghost.Name))
	g.anim = &animator{
		active:  map[string]*running{},
		pending: map[string]*time.Timer{},
		rng:     rand.New(rand.NewSource(int64(h.Sum64()))),
	}

	if g.reducedMotion {
		g.opts.Log("haunt: reduced motion: idle animation disabled")
		return
	}
	for _, c := range g.shell.Clips() {
		switch c.Trigger {
		case shell.TriggerAlways, shell.TriggerRunOnce:
			g.playClip(c.Name)
		case shell.TriggerSometimes, shell.TriggerRarely:
			g.scheduleClip(c)
		}
	}
}

// stopAnimation halts everything, so a ghost's timers do not outlive it.
func (g *Ghost) stopAnimation() {
	if g.anim == nil {
		return
	}
	g.anim.mu.Lock()
	defer g.anim.mu.Unlock()
	g.anim.stopped = true
	for _, r := range g.anim.active {
		r.timer.Stop()
	}
	for _, t := range g.anim.pending {
		t.Stop()
	}
	g.anim.active = map[string]*running{}
	g.anim.pending = map[string]*time.Timer{}
}

// scheduleClip arranges the next automatic run.
func (g *Ghost) scheduleClip(c shell.Clip) {
	d, ok := triggerDelays[c.Trigger]
	if !ok {
		return
	}
	g.anim.mu.Lock()
	defer g.anim.mu.Unlock()
	if g.anim.stopped {
		return
	}
	wait := d[0] + time.Duration(g.anim.rng.Int63n(int64(d[1]-d[0])))
	g.anim.pending[c.Name] = time.AfterFunc(wait, func() { g.playClip(c.Name) })
}

// playClip starts (or restarts) a clip by name.
func (g *Ghost) playClip(name string) {
	if g.shell == nil || g.anim == nil {
		return
	}
	c, ok := g.shell.Clip(name)
	if !ok {
		g.opts.Log(fmt.Sprintf("haunt: no clip %q in this shell", name))
		return
	}

	g.anim.mu.Lock()
	if g.anim.stopped {
		g.anim.mu.Unlock()
		return
	}
	if r, ok := g.anim.active[name]; ok {
		r.timer.Stop()
	}
	g.anim.seq++
	r := &running{clip: c, started: time.Now(), order: g.anim.seq}
	g.anim.active[name] = r
	g.anim.mu.Unlock()

	g.advanceClip(name)
}

// advanceClip applies the frame due now and schedules the next one.
func (g *Ghost) advanceClip(name string) {
	g.anim.mu.Lock()
	r, ok := g.anim.active[name]
	if !ok || g.anim.stopped {
		g.anim.mu.Unlock()
		return
	}
	elapsed := int(time.Since(r.started) / time.Millisecond)
	_, nextIn, done := r.clip.Frame(elapsed)
	if done {
		delete(g.anim.active, name)
	} else {
		r.timer = time.AfterFunc(time.Duration(nextIn)*time.Millisecond, func() {
			g.advanceClip(name)
		})
	}
	trigger := r.clip.Trigger
	g.anim.mu.Unlock()

	g.repaintAnimation()

	if !done {
		return
	}
	// The clip finished: loop it, or line up its next outing.
	switch trigger {
	case shell.TriggerAlways:
		if !g.reducedMotion {
			g.playClip(name)
		}
	case shell.TriggerSometimes, shell.TriggerRarely:
		if !g.reducedMotion {
			if c, ok := g.shell.Clip(name); ok {
				g.scheduleClip(c)
			}
		}
	}
}

// repaintAnimation composes every running clip's current frame over the
// pose and puts the result on screen.
func (g *Ghost) repaintAnimation() {
	if g.shell == nil || g.anim == nil {
		return
	}
	g.anim.mu.Lock()
	type framed struct {
		order int
		set   map[string]string
	}
	frames := make([]framed, 0, len(g.anim.active))
	for _, r := range g.anim.active {
		set, _, _ := r.clip.Frame(int(time.Since(r.started) / time.Millisecond))
		frames = append(frames, framed{r.order, set})
	}
	g.anim.mu.Unlock()

	// Newest clip wins a contested layer.
	overrides := map[string]string{}
	for pass := 0; pass < len(frames); pass++ {
		best := -1
		for i, f := range frames {
			if f.set != nil && (best < 0 || f.order < frames[best].order) {
				best = i
			}
		}
		if best < 0 {
			break
		}
		for layer, variant := range frames[best].set {
			overrides[layer] = variant
		}
		frames[best].set = nil
	}

	g.shellMu.Lock()
	g.shell.SetOverrides(overrides)
	sp := g.shell.Sprite()
	g.shellMu.Unlock()

	if g.surface == nil {
		return
	}
	if err := g.surface.Redraw(sp); err != nil {
		g.opts.Log(fmt.Sprintf("haunt: animation repaint: %v", err))
	}
}
