package ghost

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/aaronjauregui/haunt/internal/hlog"
)

// fillIndex writes memories into consecutive slots, cursor and all, the way
// remember does while the index is filling.
func fillIndex(t *testing.T, s *memStore, mems ...memory) {
	t.Helper()
	for i, m := range mems {
		entry, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Set(memKeyPrefix+strconv.Itoa(i), string(entry)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Set(memCursorKey, strconv.Itoa(len(mems))); err != nil {
		t.Fatal(err)
	}
}

// TestStrengthShape: the ordering strength imposes is the whole claim of
// "forgetting with a shape", so it is asserted as an ordering rather than
// as numbers, which are free to be retuned.
func TestStrengthShape(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }

	yesterday := memory{Kind: kindExchange, Time: ago(24 * time.Hour)}
	onceInMarch := memory{Kind: kindExchange, Time: ago(90 * 24 * time.Hour)}
	oldButUseful := memory{
		Kind: kindExchange,
		Time: ago(90 * 24 * time.Hour),
		Last: ago(2 * 24 * time.Hour),
		Uses: 8,
	}
	summary := memory{Kind: kindSummary, Time: ago(90 * 24 * time.Hour)}
	folded := memory{Kind: kindExchange, Time: ago(24 * time.Hour), Folded: true}

	// Old and never returned to is the weakest thing in the index — the
	// point of the whole exercise, since ring position would have kept it
	// and evicted yesterday instead.
	if onceInMarch.strength(now) >= yesterday.strength(now) {
		t.Error("a memory from March outranks yesterday's")
	}
	// But age alone does not condemn it: a thing that keeps coming back
	// survives a thing that arrived recently and was never relevant again.
	if oldButUseful.strength(now) <= yesterday.strength(now) {
		t.Error("a habit lost to a passing remark")
	}
	// A summary of the same age outlives its material, which is what makes
	// consolidation a ratchet.
	if summary.strength(now) <= onceInMarch.strength(now) {
		t.Error("a summary is no stickier than a raw memory of the same age")
	}
	// And of two memories the same age, the one whose substance is already
	// in a summary is the one to give up — which is what "let the raw
	// entries age out" means when the Store has no delete. (Not that a
	// folded entry ranks below *everything*: a recent one still outlives a
	// stale unfolded one, because the detail is worth keeping while there
	// is room for it.)
	if folded.strength(now) >= yesterday.strength(now) {
		t.Error("folding a memory into a summary did not weaken its claim")
	}
}

// TestForgettingEvictsTheWeakest: a full index gives up the memory with the
// least claim on its slot, not the one that happens to be oldest by
// arrival. Ring position used to decide this, which meant a daily habit was
// evicted on schedule while a one-off from March sat safe behind it.
func TestForgettingEvictsTheWeakest(t *testing.T) {
	s := &memStore{m: map[string]string{}}
	now := time.Now()

	mems := make([]memory, 0, maxMemories)
	// Slot 0 is the oldest by arrival and the one a ring would take next,
	// but it is a memory that keeps proving relevant.
	mems = append(mems, memory{
		Kind: kindExchange, Text: "the habit",
		Time: now.Add(-200 * 24 * time.Hour),
		Last: now.Add(-time.Hour), Uses: 20,
	})
	// Slot 1 arrived later and was never useful again.
	mems = append(mems, memory{
		Kind: kindExchange, Text: "the aside",
		Time: now.Add(-120 * 24 * time.Hour),
	})
	for i := len(mems); i < maxMemories; i++ {
		mems = append(mems, memory{
			Kind: kindExchange,
			Text: fmt.Sprintf("filler %d", i),
			Time: now.Add(-time.Duration(i) * time.Minute),
		})
	}
	fillIndex(t, s, mems...)

	g := &Ghost{store: s, log: hlog.Discard()}
	key, cursor := g.allocMemorySlot()
	if want := memKeyPrefix + "1"; key != want {
		t.Errorf("evicted %s, want %s (the weakest memory, not the oldest slot)", key, want)
	}
	if cursor != -1 {
		t.Errorf("cursor = %d, want -1: a full index has no next slot", cursor)
	}
}

// TestFillingUsesTheCursor: while the index has room, allocation is still
// the plain append it always was — eviction is only for a full one.
func TestFillingUsesTheCursor(t *testing.T) {
	s := &memStore{m: map[string]string{}}
	fillIndex(t, s,
		memory{Kind: kindExchange, Text: "one"},
		memory{Kind: kindExchange, Text: "two"},
	)
	g := &Ghost{store: s, log: hlog.Discard()}
	key, cursor := g.allocMemorySlot()
	if key != memKeyPrefix+"2" || cursor != 3 {
		t.Errorf("alloc = %s, cursor %d; want %s2, cursor 3", key, cursor, memKeyPrefix)
	}
}

// TestReinforceCountsUses: recall is what makes a memory prove itself, so
// it is what feeds the frequency half of forgetting.
func TestReinforceCountsUses(t *testing.T) {
	s := &memStore{m: map[string]string{}}
	fillIndex(t, s,
		memory{Kind: kindExchange, Text: "the user drinks oolong"},
		memory{Kind: kindExchange, Text: "unrelated"},
	)
	g := &Ghost{store: s, log: hlog.Discard()}

	loaded := g.loadMemories()
	var target memory
	for _, m := range loaded {
		if m.Text == "the user drinks oolong" {
			target = m
		}
	}
	g.reinforce([]memory{target})

	for _, m := range g.loadMemories() {
		switch m.Text {
		case "the user drinks oolong":
			if m.Uses != 1 || m.Last.IsZero() {
				t.Errorf("recalled memory has %d uses, last %v", m.Uses, m.Last)
			}
		default:
			if m.Uses != 0 {
				t.Errorf("a memory nobody recalled gained %d uses", m.Uses)
			}
		}
	}
}

// TestReinforceSkipsRecycledSlots: reinforcement happens off the
// conversation's path, so by the time it runs the slot may belong to
// something else. Writing the old entry back would resurrect exactly what
// the index had just decided to forget.
func TestReinforceSkipsRecycledSlots(t *testing.T) {
	s := &memStore{m: map[string]string{}}
	fillIndex(t, s, memory{Kind: kindExchange, Text: "the old occupant"})

	g := &Ghost{store: s, log: hlog.Discard()}
	stale := g.loadMemories()[0]

	// The slot is reused while the recall that returned it is still in
	// flight.
	entry, _ := json.Marshal(memory{Kind: kindExchange, Text: "the new occupant"})
	if err := s.Set(stale.key, string(entry)); err != nil {
		t.Fatal(err)
	}
	g.reinforce([]memory{stale})

	mems := g.loadMemories()
	if len(mems) != 1 || mems[0].Text != "the new occupant" || mems[0].Uses != 0 {
		t.Errorf("reinforcement resurrected a forgotten memory: %+v", mems)
	}
}
