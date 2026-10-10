package memory

// The person may edit the files while the ghost runs, and the index has
// to follow. The directory is polled, as grants.toml is: a stat of a
// few dozen small files every half second costs nothing next to a turn,
// and needs no dependency. A change is acted on once the files have held
// still for one poll, so an editor that writes a temp file, renames it
// into place and touches a backup is one reindex, not three.

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

// pollInterval is how often the files are stat'ed, and how long they
// must hold still before a change is indexed.
const pollInterval = 500 * time.Millisecond

type stamp struct {
	mod  time.Time
	size int64
}

// snapshot is the version of every file the memory is made of, by name
// relative to the memory directory.
type snapshot map[string]stamp

func (a snapshot) equal(b snapshot) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		w, ok := b[k]
		if !ok || !v.mod.Equal(w.mod) || v.size != w.size {
			return false
		}
	}
	return true
}

// restamp records rel's current version, or its absence.
func (a snapshot) restamp(dir, rel string) {
	if fi, err := os.Stat(filepath.Join(dir, rel)); err == nil {
		a[rel] = stamp{fi.ModTime(), fi.Size()}
	} else {
		delete(a, rel)
	}
}

// scan stamps self.md, you.md and the topic files. A file that cannot be
// stat'ed is left out, which reads as a change if it was there before.
func scan(dir string) snapshot {
	snap := snapshot{}
	add := func(rel string) {
		if fi, err := os.Stat(filepath.Join(dir, rel)); err == nil {
			snap[rel] = stamp{fi.ModTime(), fi.Size()}
		}
	}
	add("self.md")
	add("you.md")
	names, _ := topicNames(dir)
	for _, n := range names {
		add(filepath.Join(topicsDir, n+".md"))
	}
	return snap
}

func (s *fileStore) Watch(ctx context.Context, onChange func()) error {
	if _, err := os.Stat(s.dir); err != nil {
		return scrub(s.dir, err)
	}
	go s.follow(ctx, onChange)
	return nil
}

// follow polls until ctx is done or the store is closed. A change seen
// on one poll is indexed on the next if nothing moved in between.
func (s *fileStore) follow(ctx context.Context, onChange func()) {
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	var pending snapshot
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.closed:
			return
		case <-t.C:
		}
		cur := scan(s.dir)
		s.mu.Lock()
		switch {
		case cur.equal(s.snap):
			pending = nil
			s.mu.Unlock()
			continue
		case pending == nil || !cur.equal(pending):
			pending = cur
			s.mu.Unlock()
			continue
		}
		// Held still for a poll: index it. A failure leaves the snapshot
		// where it was, so the next poll tries again.
		_, _, err := Reindex(s.dir, s.db)
		if err == nil {
			s.snap = cur
		}
		pending = nil
		s.mu.Unlock()
		if err == nil && onChange != nil {
			onChange()
		}
	}
}
