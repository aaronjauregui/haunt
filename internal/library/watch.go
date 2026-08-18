package library

import (
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Revocation has to take effect on the ghost that is already running.
//
// A permission you can only withdraw by restarting the thing is not really
// withdrawn: the moment a person decides their ghost should stop reading
// their window titles is the moment it should stop, not next login. The
// capability check is already per-intent (Ghost.capabilityActive), so the
// only thing missing was for the answer to be allowed to change — which is
// what this is. Grants are re-read from disk when the file changes, and the
// running driver simply starts refusing.
//
// The cost is a stat, at most once per interval, on a path that is checked
// several times per exchange; the alternative was an IPC message from the
// CLI to a driver that may not be running, which is more machinery for a
// worse guarantee.
type GrantWatch struct {
	name string
	path string

	mu      sync.Mutex
	grants  *Grants
	modTime time.Time
	size    int64
	checked time.Time

	// OnChange, if set, is called with what was gained and what was lost
	// whenever the file changes under a running ghost. A revoked capability
	// should be visible in the log: it explains every refusal that follows.
	OnChange func(granted, revoked []string)
}

// grantPollInterval is how often the file is re-stat'ed at most.
const grantPollInterval = time.Second

// Watch loads a ghost's grants and follows changes to them.
func Watch(name string) (*GrantWatch, error) {
	dir, err := GhostDir(name)
	if err != nil {
		return nil, err
	}
	g, err := LoadGrants(name)
	if err != nil {
		return nil, err
	}
	w := &GrantWatch{name: name, path: filepath.Join(dir, "grants.toml"), grants: g}
	w.stamp()
	return w, nil
}

// Follow watches for changes in the background until done is closed, so a
// revocation is noticed — and reported — while the ghost is idle rather than
// at whatever moment it next happens to check. Without it the log line
// arrives after the next intent, which is exactly when the person who
// revoked something is watching for it.
func (w *GrantWatch) Follow(done <-chan struct{}) {
	t := time.NewTicker(grantPollInterval)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
		}
		w.mu.Lock()
		w.refreshLocked()
		w.mu.Unlock()
	}
}

// Granted reports whether cap is currently granted, reloading the file if
// it has changed since the last look.
func (w *GrantWatch) Granted(cap string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.refreshLocked()
	return w.grants.Granted(cap)
}

// Refused reports a standing "never ask me about this again", reloading
// like Granted does.
func (w *GrantWatch) Refused(cap string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.refreshLocked()
	return w.grants.IsRefused(cap)
}

// Grants returns the capabilities currently granted.
func (w *GrantWatch) Grants() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.refreshLocked()
	return append([]string(nil), w.grants.Capabilities...)
}

// refreshLocked re-reads the grants when the file has changed. Callers hold
// the lock.
func (w *GrantWatch) refreshLocked() {
	now := time.Now()
	if now.Sub(w.checked) < grantPollInterval {
		return
	}
	w.checked = now
	mod, size, ok := stat(w.path)
	if ok && mod.Equal(w.modTime) && size == w.size {
		return
	}
	next, err := LoadGrants(w.name)
	if err != nil {
		// Keep what we had: a half-written or briefly unreadable file must
		// not silently widen or narrow what the ghost may do.
		return
	}
	granted, revoked := diff(w.grants.Capabilities, next.Capabilities)
	w.grants = next
	w.modTime, w.size = mod, size
	if (len(granted) > 0 || len(revoked) > 0) && w.OnChange != nil {
		w.OnChange(granted, revoked)
	}
}

func (w *GrantWatch) stamp() {
	w.modTime, w.size, _ = stat(w.path)
	w.checked = time.Now()
}

// diff reports what is in next but not before, and what has been dropped.
func diff(before, next []string) (granted, revoked []string) {
	had := map[string]bool{}
	for _, c := range before {
		had[c] = true
	}
	has := map[string]bool{}
	for _, c := range next {
		has[c] = true
		if !had[c] {
			granted = append(granted, c)
		}
	}
	for _, c := range before {
		if !has[c] {
			revoked = append(revoked, c)
		}
	}
	sort.Strings(granted)
	sort.Strings(revoked)
	return granted, revoked
}

// stat is os.Stat reduced to what identifies a version of the file, and
// forgiving of it not existing — a ghost installed with nothing granted has
// no grants file at all until something is.
func stat(path string) (time.Time, int64, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, 0, false
	}
	return fi.ModTime(), fi.Size(), true
}
