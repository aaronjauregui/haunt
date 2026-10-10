// Package memory is a ghost's memory: Markdown files as the truth, an
// FTS5 table in the ghost's state.db derived from them for recall. Two
// always-present files (self.md, you.md) and a library of topic files
// whose lines are facts with provenance. The person may edit the files
// while the ghost runs; the store watches and reindexes.
//
// See docs/user/memory.md for the format and docs/dev/brain.md for how
// the brain uses it.
package memory

import (
	"context"
	"database/sql"
	"time"
)

// Provenance says where a remembered line came from.
type Provenance struct {
	Source string // person|web|file|self
	Turn   int64
	At     time.Time
}

// Hit is one recalled line.
type Hit struct {
	Topic string
	Line  string
	Prov  Provenance
	Score float64
}

// Store is a ghost's memory.
type Store interface {
	Core() (self, you string, err error)
	Index() (string, error) // one line per topic, under 1500 bytes
	Recall(ctx context.Context, q string, n int) ([]Hit, error)
	Remember(topic, line string, p Provenance) error
	Forget(topic, line string) error
	SetCore(which, text string) error // "self" or "you"
	Watch(ctx context.Context, onChange func()) error
	Close() error
}

// Open opens the memory under dir, indexing into db, the ghost's state.db.
func Open(dir string, db *sql.DB) (Store, error) { panic("unimplemented") }
