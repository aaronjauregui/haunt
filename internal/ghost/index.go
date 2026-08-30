package ghost

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"

	"github.com/aaronjauregui/haunt/internal/hlog"
)

// The resident index: the driver's own structures, held in memory, written
// through to the store.
//
// The memory ring and the entity index are not key-value access patterns.
// One is a vector index scanned linearly with an eviction policy over it;
// the other is a name-to-facts map looked up by substring. Neither is "get
// me this key", and the only read primitive a Store offers is All() — an
// unfiltered SELECT of every row. So both used to re-derive themselves from
// the whole store on every use, and an ordinary exchange did it six times:
// entityContext, recallScored, reinforce, remember's eviction scan, and
// recordEntity twice over. With a full ring that is about 2MB read, decoded
// and thrown away per call, and the cost is proportional to everything in
// the store rather than to what is being read — so the reply trace, the
// session snapshot and every key a brain has ever set are all dragged
// through a recall that wanted five memories.
//
// The fix is not a narrower read primitive. It is to stop treating the
// store as a query engine: load these two structures once, keep them, and
// write each change through to the store as it is made. The Store interface
// is left exactly as it was — three methods, still swappable for an
// encrypted or remote backend — because it was never the thing that was
// wrong.
//
// This is a working set and not a cache, and the difference is that there
// is nothing to invalidate against: a ghost is the sole writer of its own
// store. `haunt journal` and `haunt history` open the same database and
// only ever read from it, and everything that changes a running ghost's
// state arrives through its control socket. That invariant is what makes
// this sound, so it is worth stating plainly:
//
//	While a ghost is running, the resident index is authoritative. Nothing
//	may write to a ghost's memory or entity keys except through the ghost
//	itself.
//
// A second process that opened the database and wrote a memory key would
// have that write silently overwritten by the next resident flush. Sync —
// a store that receives writes from another machine — is where this has to
// be decided rather than assumed: either it lands through the ghost, or the
// Store grows a way to say "somebody else changed this".
type residentIndex struct {
	mu     sync.RWMutex
	loaded bool
	// mems and ents are keyed by store key, which is what makes a write
	// through and an eviction the same operation on both sides.
	mems map[string]memory
	ents map[string]Entity
	// cursor is the next unused slot while the ring is filling. It stops
	// meaning anything once the ring is full, at which point strength
	// chooses the slot instead (see allocMemorySlot).
	cursor int
}

// adopt seeds the index from a snapshot the caller already has in hand.
// boot reads the whole store for the brain's KV anyway, so on the ordinary
// path this is what loads the index and there is exactly one full read per
// session, at the one moment that genuinely wants everything.
func (x *residentIndex) adopt(all map[string]string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.loaded {
		return
	}
	x.fill(all)
}

// ensure loads the index from the store if adopt has not already done it.
// This is the path for anything that runs without a boot — the tests, and
// any future caller that reaches memory before the ghost is on screen.
//
// A read that fails leaves the index unloaded rather than empty, so the
// next call tries again. Consuming the load on a transient store error
// would leave the ghost quietly amnesiac and then let it write over the
// memories it had failed to read.
func (x *residentIndex) ensure(s Store) error {
	x.mu.RLock()
	loaded := x.loaded
	x.mu.RUnlock()
	if loaded {
		return nil
	}

	x.mu.Lock()
	defer x.mu.Unlock()
	if x.loaded {
		return nil
	}
	all, err := s.All()
	if err != nil {
		return err
	}
	x.fill(all)
	return nil
}

// fill parses a store snapshot into the resident structures. Callers hold
// the write lock.
func (x *residentIndex) fill(all map[string]string) {
	x.mems = make(map[string]memory, maxMemories)
	x.ents = make(map[string]Entity, maxEntities)
	x.cursor = 0
	for k, v := range all {
		switch {
		case strings.HasPrefix(k, memKeyPrefix):
			var m memory
			if json.Unmarshal([]byte(v), &m) != nil {
				continue
			}
			m.key = k
			x.mems[k] = m
		case strings.HasPrefix(k, entityKeyPrefix):
			var e Entity
			// An entity with no name is the tombstone eviction leaves
			// behind, the Store having no delete. Skipping it here is what
			// makes the slot free.
			if json.Unmarshal([]byte(v), &e) != nil || e.Name == "" {
				continue
			}
			e.key = k
			x.ents[k] = e
		case k == memCursorKey:
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				x.cursor = n
			}
		}
	}
	x.loaded = true
}

// memories returns a snapshot of the ring. A copy, because callers iterate
// it while background work may be writing: memory is all value fields, so
// copying the struct is enough to make each entry the caller's own.
func (x *residentIndex) memories() []memory {
	x.mu.RLock()
	defer x.mu.RUnlock()
	out := make([]memory, 0, len(x.mems))
	for _, m := range x.mems {
		out = append(out, m)
	}
	return out
}

// entities returns a snapshot of the subject index, deep enough to be
// safe: an Entity carries a slice of facts, and recordEntity appends to it,
// which would write into the resident copy's backing array before the
// store had accepted the change.
func (x *residentIndex) entities() []Entity {
	x.mu.RLock()
	defer x.mu.RUnlock()
	out := make([]Entity, 0, len(x.ents))
	for _, e := range x.ents {
		out = append(out, e.clone())
	}
	return out
}

// putMemory records an entry that the store has already accepted.
func (x *residentIndex) putMemory(m memory) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.mems == nil {
		x.mems = map[string]memory{}
	}
	x.mems[m.key] = m
}

// putEntity records a subject that the store has already accepted.
func (x *residentIndex) putEntity(e Entity) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.ents == nil {
		x.ents = map[string]Entity{}
	}
	x.ents[e.key] = e.clone()
}

// dropEntity forgets a subject whose slot has been given up.
func (x *residentIndex) dropEntity(key string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	delete(x.ents, key)
}

// setCursor records the next unused slot.
func (x *residentIndex) setCursor(n int) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.cursor = n
}

// nextSlot is the slot a new memory goes into while the ring is filling.
func (x *residentIndex) nextSlot() int {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.cursor % maxMemories
}

// --- the Ghost's side of it ---
//
// Every write goes to the store first and to the index second, so a store
// that refuses a write leaves the two agreeing rather than leaving the
// index ahead of what survives a restart.
//
// These take the index's lock underneath memMu, which is the pattern the
// Ghost doc comment sanctions: memMu is memory's work slot rather than a
// data lock, and the small locks are taken freely under it.

// putMemory writes one entry to the store and to the resident index. The
// entry's key says which slot it belongs in — a new one from
// allocMemorySlot, or its own for a rewrite.
func (g *Ghost) putMemory(m memory) error {
	entry, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := g.store.Set(m.key, string(entry)); err != nil {
		return err
	}
	g.index.putMemory(m)
	return nil
}

// setMemoryCursor advances the filling ring's next-slot marker.
func (g *Ghost) setMemoryCursor(n int) {
	if err := g.store.Set(memCursorKey, strconv.Itoa(n)); err != nil {
		g.log.Warn(hlog.Memory, "cursor: %v", err)
		return
	}
	g.index.setCursor(n)
}

// putEntity writes one subject to the store and to the resident index.
func (g *Ghost) putEntity(e Entity) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if err := g.store.Set(e.key, string(b)); err != nil {
		return err
	}
	g.index.putEntity(e)
	return nil
}

// dropEntity gives up a subject's slot. The Store has no delete, so the
// slot is overwritten with a value the loader refuses.
func (g *Ghost) dropEntity(key string) error {
	if err := g.store.Set(key, "{}"); err != nil {
		return err
	}
	g.index.dropEntity(key)
	return nil
}
