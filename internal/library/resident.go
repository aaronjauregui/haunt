package library

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Who is in the room. `current` named one ghost because the driver could
// only run one; the driver now runs a set, so the set is what is written
// down.
//
// Two files rather than one, and deliberately: `resident` is the set, and
// `current` is kept as the first name in it. Nothing in the driver reads
// `current` any more, but a haunt older than this one does, and a user who
// downgrades should find their ghost still selected rather than an empty
// library. It costs one line to write.
//
// The set is ordered, and the order is the user's: `haunt add` appends, so
// the first name is the one they chose first. Anything that has to pick a
// single ghost without being told which — a default screen corner, say —
// takes the first, which is a more predictable rule than sorting names.

const (
	residentFile = "resident"
	currentFile  = "current"
)

// Resident returns the ghosts that should be running, in the user's order.
// A library written by an older haunt has no set, only a selection, so the
// selection is read as a set of one.
func Resident() ([]string, error) {
	root, err := Root()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(root, residentFile))
	if os.IsNotExist(err) {
		cur, err := Current()
		if err != nil || cur == "" {
			return nil, err
		}
		return []string{cur}, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		n := strings.TrimSpace(sc.Text())
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		names = append(names, n)
	}
	return names, sc.Err()
}

// SetResident replaces the resident set. Every name must be installed:
// writing down a ghost that is not there turns a typo into a startup
// failure at next login, which is the worst time to find out.
func SetResident(names []string) error {
	root, err := Root()
	if err != nil {
		return err
	}
	var out []string
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			continue
		}
		if _, err := PkgDir(n); err != nil {
			return err
		}
		seen[n] = true
		out = append(out, n)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	body := ""
	if len(out) > 0 {
		body = strings.Join(out, "\n") + "\n"
	}
	if err := os.WriteFile(filepath.Join(root, residentFile), []byte(body), 0o644); err != nil {
		return err
	}
	// Kept in step for anything that still reads a single selection.
	if len(out) == 0 {
		os.Remove(filepath.Join(root, currentFile))
		return nil
	}
	return os.WriteFile(filepath.Join(root, currentFile), []byte(out[0]+"\n"), 0o644)
}

// AddResident makes a ghost resident, reporting whether it was not already.
func AddResident(name string) (added bool, err error) {
	cur, err := Resident()
	if err != nil {
		return false, err
	}
	for _, n := range cur {
		if n == name {
			return false, nil
		}
	}
	return true, SetResident(append(cur, name))
}

// DropResident takes a ghost out of the room without uninstalling it: its
// memory, grants and package stay where they are, and adding it back picks
// up where it left off. That distinction is the whole reason this is not
// `haunt rm`.
func DropResident(name string) (dropped bool, err error) {
	cur, err := Resident()
	if err != nil {
		return false, err
	}
	var out []string
	for _, n := range cur {
		if n != name {
			out = append(out, n)
		}
	}
	if len(out) == len(cur) {
		return false, fmt.Errorf("ghost %q is not resident", name)
	}
	return true, SetResident(out)
}

// IsResident reports whether a ghost is in the resident set.
func IsResident(name string) (bool, error) {
	names, err := Resident()
	if err != nil {
		return false, err
	}
	for _, n := range names {
		if n == name {
			return true, nil
		}
	}
	return false, nil
}

// writeResident replaces the set without checking the names are installed.
// Only removal wants this: everything else should fail on a name that is
// not there.
func writeResident(names []string) error {
	root, err := Root()
	if err != nil {
		return err
	}
	body := ""
	if len(names) > 0 {
		body = strings.Join(names, "\n") + "\n"
	}
	if err := os.WriteFile(filepath.Join(root, residentFile), []byte(body), 0o644); err != nil {
		return err
	}
	if len(names) == 0 {
		os.Remove(filepath.Join(root, currentFile))
		return nil
	}
	return os.WriteFile(filepath.Join(root, currentFile), []byte(names[0]+"\n"), 0o644)
}
