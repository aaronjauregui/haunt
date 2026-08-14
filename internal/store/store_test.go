package store

import (
	"path/filepath"
	"testing"
)

func TestKVRoundtripAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ghost.db")

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("visits", "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("visits", "2"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("mood", "curious"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("mood"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen: state survives.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	v, ok, err := s2.Get("visits")
	if err != nil || !ok || v != "2" {
		t.Fatalf("Get(visits) = %q, %v, %v; want 2, true, nil", v, ok, err)
	}
	if _, ok, _ := s2.Get("mood"); ok {
		t.Error("deleted key still present")
	}
	all, err := s2.All()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all["visits"] != "2" {
		t.Errorf("All() = %v, want map[visits:2]", all)
	}
}

func TestGetMissingKey(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ghost.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, ok, err := s.Get("nope"); ok || err != nil {
		t.Fatalf("missing key: ok=%v err=%v, want false, nil", ok, err)
	}
}
