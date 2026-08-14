package ghost

import (
	"path/filepath"
	"testing"

	"github.com/aaronjauregui/haunt/internal/store"
)

// The SQLite backend satisfies the pluggable Store contract.
var _ Store = (*store.Store)(nil)

// memStore is a trivial alternative backend, proving the interface is
// sufficient for a full session's needs.
type memStore struct{ m map[string]string }

func (s *memStore) Set(k, v string) error           { s.m[k] = v; return nil }
func (s *memStore) All() (map[string]string, error) { return s.m, nil }
func (s *memStore) Close() error                    { return nil }

var _ Store = (*memStore)(nil)

func TestSQLiteSatisfiesStore(t *testing.T) {
	var s Store
	sq, err := store.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	s = sq
	if err := s.Set("k", "v"); err != nil {
		t.Fatal(err)
	}
	all, err := s.All()
	if err != nil || all["k"] != "v" {
		t.Fatalf("All() = %v, %v", all, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
