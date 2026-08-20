package library

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// pretendInstalled makes a ghost look installed: the resident set refuses
// names that are not, so the tests need something for them to point at.
func pretendInstalled(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		dir, err := GhostDir(n)
		if err != nil {
			t.Fatal(err)
		}
		pkg := filepath.Join(dir, "pkg")
		if err := os.MkdirAll(pkg, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "[ghost]\nname = \"" + n + "\"\nversion = \"0.1.0\"\n"
		if err := os.WriteFile(filepath.Join(pkg, "ghost.toml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAddAndDropResident(t *testing.T) {
	t.Setenv("HAUNT_DATA_DIR", t.TempDir())
	pretendInstalled(t, "wisp", "moth", "shade")

	if names, err := Resident(); err != nil || len(names) != 0 {
		t.Fatalf("a fresh library has nobody in it, got %v (%v)", names, err)
	}
	for _, n := range []string{"wisp", "moth", "shade"} {
		if added, err := AddResident(n); err != nil || !added {
			t.Fatalf("adding %s: added=%v err=%v", n, added, err)
		}
	}
	// The order is the user's: add appends.
	if names, _ := Resident(); !reflect.DeepEqual(names, []string{"wisp", "moth", "shade"}) {
		t.Errorf("resident set is %v, want the order they were added in", names)
	}
	if added, err := AddResident("moth"); err != nil || added {
		t.Errorf("adding a resident ghost again should be a no-op, got added=%v err=%v", added, err)
	}

	if _, err := DropResident("moth"); err != nil {
		t.Fatal(err)
	}
	if names, _ := Resident(); !reflect.DeepEqual(names, []string{"wisp", "shade"}) {
		t.Errorf("resident set is %v after dropping moth", names)
	}
	if _, err := DropResident("moth"); err == nil {
		t.Error("dropping a ghost that is not resident should say so")
	}
}

// Residency is not installation: dropping a ghost keeps its memory.
func TestDropKeepsTheGhostInstalled(t *testing.T) {
	t.Setenv("HAUNT_DATA_DIR", t.TempDir())
	pretendInstalled(t, "wisp")
	if _, err := AddResident("wisp"); err != nil {
		t.Fatal(err)
	}
	if _, err := DropResident("wisp"); err != nil {
		t.Fatal(err)
	}
	if _, err := PkgDir("wisp"); err != nil {
		t.Errorf("drop uninstalled the ghost: %v", err)
	}
	if added, err := AddResident("wisp"); err != nil || !added {
		t.Errorf("a dropped ghost should be addable again: added=%v err=%v", added, err)
	}
}

// A typo must not become a startup failure at next login.
func TestResidentRefusesAGhostThatIsNotInstalled(t *testing.T) {
	t.Setenv("HAUNT_DATA_DIR", t.TempDir())
	if _, err := AddResident("nobody"); err == nil {
		t.Error("adding an uninstalled ghost should fail")
	}
}

// switch means "this one instead", not "this one as well".
func TestSwitchReplacesTheSet(t *testing.T) {
	t.Setenv("HAUNT_DATA_DIR", t.TempDir())
	pretendInstalled(t, "wisp", "moth")
	AddResident("wisp")
	AddResident("moth")
	if err := SetCurrent("moth"); err != nil {
		t.Fatal(err)
	}
	if names, _ := Resident(); !reflect.DeepEqual(names, []string{"moth"}) {
		t.Errorf("after switch the set is %v, want just moth", names)
	}
	if cur, _ := Current(); cur != "moth" {
		t.Errorf("current is %q, want moth", cur)
	}
}

// A library written before there was a set has one ghost selected, and it
// should still be the ghost that runs.
func TestAnOldLibraryReadsItsSelectionAsASet(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HAUNT_DATA_DIR", root)
	pretendInstalled(t, "wisp")
	if err := os.WriteFile(filepath.Join(root, currentFile), []byte("wisp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if names, _ := Resident(); !reflect.DeepEqual(names, []string{"wisp"}) {
		t.Errorf("resident set is %v, want the old selection", names)
	}
}

// And a haunt older than this one still finds its selection afterwards.
func TestCurrentIsKeptInStep(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HAUNT_DATA_DIR", root)
	pretendInstalled(t, "wisp", "moth")
	AddResident("wisp")
	AddResident("moth")
	data, err := os.ReadFile(filepath.Join(root, currentFile))
	if err != nil || string(data) != "wisp\n" {
		t.Errorf("current file is %q (%v), want the first resident", data, err)
	}
}

// Uninstalling implies leaving the room; leaving it written down would
// fail the next login on a ghost that is no longer there.
func TestRemoveDropsResidency(t *testing.T) {
	t.Setenv("HAUNT_DATA_DIR", t.TempDir())
	pretendInstalled(t, "wisp", "moth")
	AddResident("wisp")
	AddResident("moth")
	if err := Remove("wisp"); err != nil {
		t.Fatal(err)
	}
	if names, _ := Resident(); !reflect.DeepEqual(names, []string{"moth"}) {
		t.Errorf("resident set is %v after removing wisp", names)
	}
}
