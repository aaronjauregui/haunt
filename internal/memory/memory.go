// Package memory is a ghost's memory: Markdown files as the truth, an
// FTS5 table in the ghost's state.db derived from them for recall. Two
// always-present files (self.md, you.md) and a library of topic files
// whose lines are facts with provenance. The person may edit the files
// while the ghost runs; the store watches and reindexes.
//
// Layout under the memory directory:
//
//	self.md            the ghost's notes on itself, free prose
//	you.md             the ghost's notes on the person, free prose
//	topics/<topic>.md  one topic: front matter, then one fact per line
//
// A topic file looks like this:
//
//	---
//	topic: food
//	updated: 2026-10-10T09:12:00+10:00
//	---
//	- likes apples <!-- person t=412 2026-10-09 -->
//	- allergic to cats <!-- person t=98 2026-09-30 -->
//
// The provenance comment is invisible in a Markdown viewer and parsed by
// the store: the source (person, web, file or self), the turn and the
// date. A line the person typed without one is read as the person's, turn
// 0, since only the person writes to the files by hand. Every non-blank
// body line that is not a heading is a fact; a leading "- " or "* " is
// the bullet, not part of the fact.
//
// Facts live as lines inside topics, never one file per fact, so the
// topic index in the prompt stays small. The FTS5 table is derived: it is
// dropped and rebuilt from the files on open and whenever they change, so
// it never holds anything the files do not. Nothing this package returns
// (hits, the index, errors) names the directory it lives in; the model
// sees topic names, not paths on the person's disk.
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
	// Core returns self.md and you.md, each cut to CoreLimit bytes; a
	// missing file is empty.
	Core() (self, you string, err error)
	// Index returns one line per topic, under IndexLimit bytes.
	Index() (string, error)
	// Recall returns the n facts that best match q, by BM25 over the line.
	Recall(ctx context.Context, q string, n int) ([]Hit, error)
	// Remember appends a fact to a topic, creating the topic if needed.
	Remember(topic, line string, p Provenance) error
	// Forget removes a fact from a topic; an empty line removes the topic.
	Forget(topic, line string) error
	// SetCore replaces self.md ("self") or you.md ("you").
	SetCore(which, text string) error // "self" or "you"
	// Watch follows the directory in the background until ctx is done or
	// the store is closed, reindexing on change and then calling onChange.
	Watch(ctx context.Context, onChange func()) error
	// Close stops any watch. It does not close the database, which is the
	// caller's.
	Close() error
}

// Open opens the memory under dir, indexing into db, the ghost's state.db.
// It creates the directory if needed and rebuilds the index from the
// files. db must be the one writer's handle: the ghost's own, or the
// CLI's when no ghost is running.
func Open(dir string, db *sql.DB) (Store, error) {
	return open(dir, db)
}
