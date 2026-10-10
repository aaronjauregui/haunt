// Package store is the driver-owned persistence layer: one SQLite file per
// ghost holding its key-value memory. Brains never touch the file — they
// read state from the boot event and write it through kv_set intents.
package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Store is a ghost's persistent state.
type Store struct {
	db *sql.DB
}

// Open creates or opens the SQLite database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// The driver is the only writer; a single connection avoids lock churn.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
		PRAGMA journal_mode = WAL;
		CREATE TABLE IF NOT EXISTS kv (
			key        TEXT PRIMARY KEY,
			value      TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("initializing store schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Set writes key=value, replacing any existing value.
func (s *Store) Set(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO kv (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

// Get returns the value for key, or ok=false if absent.
func (s *Store) Get(key string) (value string, ok bool, err error) {
	err = s.db.QueryRow(`SELECT value FROM kv WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// Delete removes key if present.
func (s *Store) Delete(key string) error {
	_, err := s.db.Exec(`DELETE FROM kv WHERE key = ?`, key)
	return err
}

// All returns the complete KV map, as handed to brains at boot.
func (s *Store) All() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key, value FROM kv`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		m[k] = v
	}
	return m, rows.Err()
}

// DB is the open database, for packages that keep their own tables in
// the ghost's state.db (internal/memory) on the one writer's handle.
func (s *Store) DB() *sql.DB { return s.db }

// Close flushes and closes the database.
func (s *Store) Close() error { return s.db.Close() }
