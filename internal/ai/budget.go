package ai

import (
	"fmt"
	"sync"
	"time"
)

// budget is a sliding-window rate limiter: at most n spends per hour.
type budget struct {
	mu     sync.Mutex
	limit  int
	spends []time.Time
	now    func() time.Time
}

func newBudget(limit int) *budget {
	return &budget{limit: limit, now: time.Now}
}

// spend consumes one unit or fails with a wait hint.
func (b *budget) spend() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	cutoff := b.now().Add(-time.Hour)
	kept := b.spends[:0]
	for _, t := range b.spends {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	b.spends = kept
	if len(b.spends) >= b.limit {
		wait := time.Until(b.spends[0].Add(time.Hour))
		if w := b.spends[0].Add(time.Hour).Sub(b.now()); w > 0 {
			wait = w
		}
		return fmt.Errorf("limit of %d requests/hour reached; try again in %s", b.limit, wait.Round(time.Second))
	}
	b.spends = append(b.spends, b.now())
	return nil
}
