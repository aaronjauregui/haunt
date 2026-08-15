package ghost

import (
	"fmt"
	"hash/fnv"
	"math/rand"
	"sort"
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

// sampleInterval is how often a clip that interpolates is resampled: 15fps,
// which is plenty for a 2D companion and a quarter of the cost of 60. Clips
// that only swap variants are not sampled at all — they sleep from one
// keyframe to the next.
const sampleInterval = 66 * time.Millisecond

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

	// talking is true while dialogue is arriving; talkTimer ends it for
	// text that did not stream.
	talking   bool
	talkTimer *time.Timer
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
		// Talk clips are started by dialogue, not by the clock.
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
	if g.anim.talkTimer != nil {
		g.anim.talkTimer.Stop()
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
	_, _, nextIn, done := r.clip.FrameAt(elapsed)
	if done {
		delete(g.anim.active, name)
	} else {
		// A clip that only swaps variants sleeps until its next keyframe.
		// One that interpolates has to be sampled, but no faster than the
		// eye needs.
		wake := time.Duration(nextIn) * time.Millisecond
		if r.clip.Animates() && wake > sampleInterval {
			wake = sampleInterval
		}
		r.timer = time.AfterFunc(wake, func() { g.advanceClip(name) })
	}
	trigger := r.clip.Trigger
	g.anim.mu.Unlock()

	g.repaintAnimation()

	if !done {
		return
	}
	// The clip finished: loop it, or line up its next outing.
	switch trigger {
	case shell.TriggerTalk:
		g.anim.mu.Lock()
		still := g.anim.talking && !g.anim.stopped
		g.anim.mu.Unlock()
		if still {
			g.playClip(name)
		}
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
		move  map[string]shell.Transform
	}
	frames := make([]framed, 0, len(g.anim.active))
	for _, r := range g.anim.active {
		set, move, _, _ := r.clip.FrameAt(int(time.Since(r.started) / time.Millisecond))
		frames = append(frames, framed{r.order, set, move})
	}
	g.anim.mu.Unlock()

	// Oldest first, so the newest clip's opinion lands last and wins a
	// contested layer.
	sort.Slice(frames, func(i, j int) bool { return frames[i].order < frames[j].order })
	overrides := map[string]string{}
	moves := map[string]shell.Transform{}
	for _, f := range frames {
		for layer, variant := range f.set {
			overrides[layer] = variant
		}
		for layer, t := range f.move {
			moves[layer] = t
		}
	}

	g.shellMu.Lock()
	prev := g.painted
	g.shell.SetMoves(moves)
	g.shell.SetOverrides(overrides)
	sp := g.shell.Sprite()
	// Only the layers whose variant changed need to reach the compositor:
	// a blink is a repaint of two eyes, not of a ghost.
	damage := g.shell.Damage(prev)
	g.painted = g.shell.State()
	g.shellMu.Unlock()

	if g.surface == nil || damage.Empty() {
		return
	}
	if err := g.surface.RedrawRect(sp, damage); err != nil {
		g.opts.Log(fmt.Sprintf("haunt: animation repaint: %v", err))
	}
}

// Talking. SERIKO drove mouth movement from how much the ghost was saying,
// and it is still the right hook: a clip triggered by `talk` loops for
// exactly as long as there are words arriving, whether they come from a
// script or a model streaming a token at a time.
//
// Reduced motion covers this too. A moving mouth is decoration on top of
// the words, and the words are still there without it.

// talkRate is how long a character of scripted dialogue is "spoken" for,
// used to guess how long a mouth should move when there is no stream to
// follow. Bounded, because neither a grunt nor an essay should be taken
// literally.
const (
	talkRate = 45 * time.Millisecond
	talkMin  = 500 * time.Millisecond
	talkMax  = 6 * time.Second
)

// startTalking runs every `talk` clip until stopTalking.
func (g *Ghost) startTalking() {
	if g.anim == nil || g.reducedMotion {
		return
	}
	g.anim.mu.Lock()
	g.anim.talking = true
	g.anim.mu.Unlock()
	for _, c := range g.shell.Clips() {
		if c.Trigger == shell.TriggerTalk {
			g.playClip(c.Name)
		}
	}
}

// stopTalking ends them, letting the pose's own mouth return.
func (g *Ghost) stopTalking() {
	if g.anim == nil {
		return
	}
	g.anim.mu.Lock()
	g.anim.talking = false
	var stop []string
	for name, r := range g.anim.active {
		if r.clip.Trigger == shell.TriggerTalk {
			r.timer.Stop()
			stop = append(stop, name)
		}
	}
	for _, name := range stop {
		delete(g.anim.active, name)
	}
	g.anim.mu.Unlock()

	if len(stop) > 0 {
		g.repaintAnimation()
	}
}

// talkFor moves the mouth for a stretch proportional to the text, for
// dialogue that arrives all at once rather than streaming.
func (g *Ghost) talkFor(text string) {
	if g.anim == nil || g.reducedMotion {
		return
	}
	d := time.Duration(len([]rune(text))) * talkRate
	if d < talkMin {
		d = talkMin
	}
	if d > talkMax {
		d = talkMax
	}
	g.startTalking()

	g.anim.mu.Lock()
	if g.anim.talkTimer != nil {
		g.anim.talkTimer.Stop()
	}
	g.anim.talkTimer = time.AfterFunc(d, g.stopTalking)
	g.anim.mu.Unlock()
}
