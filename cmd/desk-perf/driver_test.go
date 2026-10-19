package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// Diagnostic instrumentation must not change transaction visibility, rollback,
// duplicate handling, or cancellation compared with the underlying driver.
func TestMeteredDriverPreservesSQLiteTransactions(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "copy.chrono") + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	writer, err := sql.Open("deskperf_sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	writer.SetMaxOpenConns(1)
	reader, err := sql.Open("deskperf_sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	reader.SetMaxOpenConns(1)
	for _, q := range []string{"CREATE TABLE facts (id TEXT PRIMARY KEY, value INTEGER)", "INSERT INTO facts VALUES ('one', 1)"} {
		if _, err := writer.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := reader.Exec("PRAGMA query_only=ON"); err != nil {
		t.Fatal(err)
	}
	tx, err := writer.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("UPDATE facts SET value=2 WHERE id='one'"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := reader.QueryRow("SELECT value FROM facts WHERE id='one'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("reader must see committed value while writer is active: n=%d err=%v", n, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	stmt, err := writer.Prepare("INSERT OR IGNORE INTO facts VALUES (?, ?)")
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	r, err := stmt.Exec("one", 99)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := r.RowsAffected(); err != nil || changed != 0 {
		t.Fatalf("duplicate changed immutable fact: changed=%d err=%v", changed, err)
	}
	if err := reader.QueryRow("SELECT value FROM facts WHERE id='one'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("rollback/duplicate changed fact: n=%d err=%v", n, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := writer.QueryContext(ctx, "SELECT value FROM facts"); err == nil {
		t.Fatal("cancelled query succeeded")
	}
	if _, err := reader.Exec("UPDATE facts SET value=3"); err == nil {
		t.Fatal("query-only connection allowed a write")
	}
}
