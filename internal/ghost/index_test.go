package ghost

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
)

// countingStore is a memStore that says how often it has been read whole,
// and can be made to refuse writes or reads.
type countingStore struct {
	m        map[string]string
	reads    int
	failRead error
	failSet  error
}

func newCounting() *countingStore { return &countingStore{m: map[string]string{}} }

func (s *countingStore) Set(k, v string) error {
	if s.failSet != nil {
		return s.failSet
	}
	s.m[k] = v
	return nil
}

// Get is a single-key read and is not counted: the point of the counter is
// how often the whole store is pulled through memory.
func (s *countingStore) Get(k string) (string, bool, error) {
	if s.failRead != nil {
		return "", false, s.failRead
	}
	v, ok := s.m[k]
	return v, ok, nil
}

func (s *countingStore) All() (map[string]string, error) {
	if s.failRead != nil {
		return nil, s.failRead
	}
	s.reads++
	out := make(map[string]string, len(s.m))
	for k, v := range s.m {
		out[k] = v
	}
	return out, nil
}

func (s *countingStore) Close() error { return nil }

var _ Store = (*countingStore)(nil)

// The whole point of the change. An exchange used to re-derive the memory
// ring and the entity index from the whole store six times over —
// entityContext, recall, reinforce, remember's eviction scan, and
// recordEntity twice — which meant every recall read the reply trace, the
// session snapshot and every key a brain had ever set. The index is loaded
// once and never again.
func TestTheStoreIsReadOnceHoweverMuchIsAsked(t *testing.T) {
	s := newCounting()
	g := &Ghost{store: s, log: hlog.Discard()}

	// Populate the way a running ghost does.
	for i := 0; i < 20; i++ {
		if err := g.putMemory(memory{
			key: memKeyPrefix + strconv.Itoa(i), Kind: kindExchange,
			Text: "exchange " + strconv.Itoa(i), Time: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	g.recordEntity("Sam", "person", FactInference, []string{"likes oolong"})

	before := s.reads
	for i := 0; i < 50; i++ {
		g.loadMemories()
		g.loadEntities()
		g.knownIn("what about Sam")
		g.allocMemorySlot()
	}
	if s.reads != before {
		t.Errorf("%d whole-store reads across 200 index queries, want 0",
			s.reads-before)
	}
	if before > 1 {
		t.Errorf("index loaded %d times, want at most 1", before)
	}
}

// boot already reads the whole store for the brain's KV snapshot, so the
// index costs nothing to load: adopt takes that snapshot rather than asking
// for another.
func TestBootSnapshotLoadsTheIndex(t *testing.T) {
	s := newCounting()
	s.m[memKeyPrefix+"0"] = `{"k":"exchange","x":"remembered before boot"}`
	s.m[entityKeyPrefix+"sam"] = `{"n":"Sam","f":["likes oolong"]}`
	s.m[memCursorKey] = "1"

	all, err := s.All()
	if err != nil {
		t.Fatal(err)
	}
	g := &Ghost{store: s, log: hlog.Discard()}
	g.index.adopt(all)

	reads := s.reads
	if mems := g.loadMemories(); len(mems) != 1 || mems[0].Text != "remembered before boot" {
		t.Errorf("memories after adopt = %+v", mems)
	}
	if ents := g.loadEntities(); len(ents) != 1 || ents[0].Name != "Sam" {
		t.Errorf("entities after adopt = %+v", ents)
	}
	if g.nextMemorySlot() != 1 {
		t.Errorf("cursor %d after adopt, want 1", g.nextMemorySlot())
	}
	if s.reads != reads {
		t.Errorf("adopt was followed by %d more reads, want 0", s.reads-reads)
	}
}

// A store that refuses a write must leave the index agreeing with what
// survives a restart. An index that ran ahead of the store would have the
// ghost remembering something for one session and then losing it without
// ever saying so.
func TestARefusedWriteDoesNotEnterTheIndex(t *testing.T) {
	s := newCounting()
	g := &Ghost{store: s, log: hlog.Discard()}

	s.failSet = errors.New("disk full")
	if err := g.putMemory(memory{key: memKeyPrefix + "0", Text: "lost"}); err == nil {
		t.Fatal("a refused write reported success")
	}
	if err := g.putEntity(Entity{key: entityKeyPrefix + "sam", Name: "Sam"}); err == nil {
		t.Fatal("a refused entity write reported success")
	}
	if got := g.loadMemories(); len(got) != 0 {
		t.Errorf("index holds %d memories the store refused", len(got))
	}
	if got := g.loadEntities(); len(got) != 0 {
		t.Errorf("index holds %d entities the store refused", len(got))
	}
}

// A read that fails must leave the index unloaded rather than empty.
// Consuming the load on a transient error would leave the ghost quietly
// amnesiac and then let it write over the memories it had failed to read.
func TestAFailedLoadIsRetried(t *testing.T) {
	s := newCounting()
	s.m[memKeyPrefix+"0"] = `{"k":"exchange","x":"still there"}`
	g := &Ghost{store: s, log: hlog.Discard()}

	s.failRead = errors.New("database is locked")
	if got := g.loadMemories(); got != nil {
		t.Errorf("a failed read returned %v, want nothing", got)
	}

	s.failRead = nil
	got := g.loadMemories()
	if len(got) != 1 || got[0].Text != "still there" {
		t.Errorf("after the store recovered, memories = %+v", got)
	}
}

// An entity handed out carries a slice of facts, and the resident copy
// must not share its array. Everything else on an Entity is a value and
// copies with the struct; Facts is the one field where a caller writing to
// what it was given would be writing into the index — a subject silently
// changing what the ghost believes about it, without the store ever having
// agreed to the change.
func TestEntitiesAreHandedOutUnshared(t *testing.T) {
	s := newCounting()
	g := &Ghost{store: s, log: hlog.Discard()}
	g.recordEntity("Sam", "person", FactInference, []string{"likes oolong", "runs on Tuesdays"})

	first := g.loadEntities()
	if len(first) != 1 || len(first[0].Facts) != 2 {
		t.Fatalf("entities = %+v", first)
	}
	first[0].Facts[0].Text = "forged"

	again := g.loadEntities()
	if again[0].Facts[0].Text != "likes oolong" {
		t.Errorf("a caller's edit reached the index: facts = %+v", again[0].Facts)
	}
}

// Eviction has to reach both sides: the store keeps the slot, because it
// has no delete, and the index must stop answering about it.
func TestEvictingASubjectClearsBothSides(t *testing.T) {
	s := newCounting()
	g := &Ghost{store: s, log: hlog.Discard()}
	g.recordEntity("Sam", "person", FactInference, []string{"likes oolong"})
	key := entityKeyPrefix + "sam"

	if err := g.dropEntity(key); err != nil {
		t.Fatal(err)
	}
	if got := g.loadEntities(); len(got) != 0 {
		t.Errorf("index still answers about %+v", got)
	}
	if s.m[key] != "{}" {
		t.Errorf("store slot = %q, want the tombstone the loader refuses", s.m[key])
	}
}
