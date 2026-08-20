package ghost

import (
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/shell"
)

// A shell that states how often a clip should run gets that rate, jittered.
// The two named triggers are the right granularity for a person guessing;
// they are the wrong one for a converter that read a number.
func TestClipDelayPrefersTheShellsOwnRate(t *testing.T) {
	lo, hi, ok := clipDelay(shell.Clip{Trigger: shell.TriggerSometimes, EveryMS: 2000})
	if !ok {
		t.Fatal("a stated rate was refused")
	}
	if lo != time.Second || hi != 3*time.Second {
		t.Errorf("range %v-%v, want 1s-3s around a 2s rate", lo, hi)
	}
	// Jittered, not metronomic: the formats this comes from express these
	// as a probability per second, and a face on the beat reads as a
	// machine.
	if lo == hi {
		t.Error("a stated rate should still wander")
	}
}

func TestClipDelayFallsBackToTheTrigger(t *testing.T) {
	lo, hi, ok := clipDelay(shell.Clip{Trigger: shell.TriggerSometimes})
	if !ok || lo != 3*time.Second || hi != 12*time.Second {
		t.Errorf("range %v-%v ok=%v, want the sometimes bucket", lo, hi, ok)
	}
	if _, _, ok := clipDelay(shell.Clip{Trigger: shell.TriggerTalk}); ok {
		t.Error("a talk clip is not on a clock and should not be scheduled")
	}
	if _, _, ok := clipDelay(shell.Clip{Trigger: shell.TriggerManual}); ok {
		t.Error("a manual clip should not be scheduled")
	}
}

// A shell asking to blink twenty times a second is asking for a strobe, and
// the driver composites and repaints on every frame of one.
func TestClipDelayHasAFloor(t *testing.T) {
	lo, _, ok := clipDelay(shell.Clip{Trigger: shell.TriggerSometimes, EveryMS: 50})
	if !ok {
		t.Fatal("refused")
	}
	if lo < minClipDelay/2 {
		t.Errorf("floor not applied: lo = %v", lo)
	}
}
