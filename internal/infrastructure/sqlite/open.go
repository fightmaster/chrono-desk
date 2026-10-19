// Package sqlite persists one event per database file using the pure-Go
// modernc.org/sqlite driver (no CGO).
package sqlite

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// Open opens (or creates) an event database file and applies pragmas suited
// for a single-process desktop app.
func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	// One writer connection serializes transitions. UI snapshots use a distinct
	// query-only pool; WAL alone cannot make this writer handle concurrent.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", path, err)
	}
	return db, nil
}

// OpenReadOnly opens a separate bounded reader pool after the writer has applied
// schema/migrations and enabled WAL. Pragmas apply to every new connection.
func OpenReadOnly(path string, connections int) (*sql.DB, error) {
	if connections < 1 {
		return nil, fmt.Errorf("reader connection count must be positive")
	}
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=query_only(1)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open read-only sqlite %s: %w", path, err)
	}
	db.SetMaxOpenConns(connections)
	db.SetMaxIdleConns(connections)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping read-only sqlite %s: %w", path, err)
	}
	return db, nil
}
