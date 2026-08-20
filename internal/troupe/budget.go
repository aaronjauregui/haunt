package troupe

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
)

// The household ceiling. Each ghost rations its own interruptions against
// its own allowance, which was the whole story while there was one ghost.
// Three ghosts each staying politely within twelve unprompted actions a day
// is thirty-six interruptions and three times the model bill, and none of
// them did anything wrong. So there is a second allowance above the first,
// spent from by whichever ghost gets there.
//
// The default is deliberately sub-linear: a second ghost should add
// presence, not double it. One ghost gets the configured allowance; each
// one after that adds half of it. Twelve becomes eighteen for two and
// twenty-four for three, rather than twenty-four and thirty-six.

// roomFile holds the shared spend log, next to the ghosts rather than
// inside any one of them: it does not belong to a ghost, and a ghost being
// uninstalled must not refill it for everybody else.
const roomFile = "room-curiosity.json"

// Ceiling returns the room's daily allowance given the per-ghost allowance
// and how many ghosts are resident. An explicit setting from the config
// (>= 0) wins outright.
func Ceiling(perGhost, configured, residents int) int {
	if configured >= 0 {
		return configured
	}
	if residents < 1 {
		residents = 1
	}
	return perGhost + perGhost*(residents-1)/2
}

// Budget is a persisted daily allowance shared by every resident ghost.
type Budget struct {
	mu     sync.Mutex
	limit  int
	path   string
	spends []time.Time
	log    *hlog.Logger
	now    func() time.Time
}

// NewBudget opens the shared allowance, reading whatever has been spent
// today. A limit of zero switches unprompted action off for the whole room.
func NewBudget(dir string, limit int, log *hlog.Logger) *Budget {
	b := &Budget{limit: limit, path: filepath.Join(dir, roomFile), log: log, now: time.Now}
	if data, err := os.ReadFile(b.path); err == nil {
		json.Unmarshal(data, &b.spends)
	}
	return b
}

// Spend consumes one action from the room's allowance on a ghost's behalf,
// reporting whether it was allowed.
func (b *Budget) Spend(who, what string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	cutoff := b.now().Add(-24 * time.Hour)
	kept := b.spends[:0]
	for _, t := range b.spends {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	b.spends = kept

	if len(b.spends) >= b.limit {
		return false
	}
	b.spends = append(b.spends, b.now())
	b.persist()
	b.log.Debug(hlog.Curiosity, "%s spent one of the room's %d (%d left today)",
		who, b.limit, b.limit-len(b.spends))
	return true
}

// persist writes the spend log; callers hold the lock.
func (b *Budget) persist() {
	data, err := json.Marshal(b.spends)
	if err != nil {
		return
	}
	if err := os.WriteFile(b.path, data, 0o644); err != nil {
		b.log.Warn(hlog.Curiosity, "persisting the room's spend: %v", err)
	}
}
