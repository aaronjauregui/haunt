package troupe

import (
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
)

// A second ghost should add presence, not double it.
func TestCeilingIsSubLinear(t *testing.T) {
	for _, tc := range []struct{ residents, want int }{
		{1, 12}, {2, 18}, {3, 24}, {4, 30},
	} {
		if got := Ceiling(12, -1, tc.residents); got != tc.want {
			t.Errorf("%d ghosts: ceiling %d, want %d", tc.residents, got, tc.want)
		}
	}
}

// The user's own number wins, however many ghosts there are.
func TestConfiguredCeilingWins(t *testing.T) {
	if got := Ceiling(12, 20, 5); got != 20 {
		t.Errorf("ceiling %d, want the configured 20", got)
	}
	// Including zero, which means "none of you may interrupt me".
	if got := Ceiling(12, 0, 5); got != 0 {
		t.Errorf("ceiling %d, want 0", got)
	}
}

func TestBudgetIsSharedAndPersisted(t *testing.T) {
	dir := t.TempDir()
	b := NewBudget(dir, 3, hlog.Discard())
	if !b.Spend("wisp", "say") || !b.Spend("moth", "say") {
		t.Fatal("the first spends should be allowed")
	}
	if !b.Spend("wisp", "say") {
		t.Fatal("the third spend should be allowed")
	}
	if b.Spend("moth", "say") {
		t.Error("the fourth spend should be refused: the room is out")
	}

	// Restarting must not refill it, which is the whole reason it is on
	// disk rather than in memory.
	again := NewBudget(dir, 3, hlog.Discard())
	if again.Spend("wisp", "say") {
		t.Error("a restart refilled the room's allowance")
	}
}

// Yesterday's spends do not count against today.
func TestBudgetForgetsYesterday(t *testing.T) {
	b := NewBudget(t.TempDir(), 1, hlog.Discard())
	b.spends = []time.Time{time.Now().Add(-25 * time.Hour)}
	if !b.Spend("wisp", "say") {
		t.Error("a spend from more than a day ago should not still count")
	}
}
