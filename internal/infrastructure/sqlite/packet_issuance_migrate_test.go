package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestPacketSiteBaselineMigrationBackfillsOnlyHistoryFreeEvents(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "packet-baseline.chrono"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE events (id TEXT PRIMARY KEY);
		CREATE TABLE packet_issuance_registrations (
			registration_id TEXT PRIMARY KEY,event_id TEXT NOT NULL,value_json TEXT NOT NULL,deleted INTEGER NOT NULL DEFAULT 0);
		CREATE TABLE packet_issuance_operations (event_id TEXT NOT NULL);
		CREATE TABLE packet_issuance_site_feed_actions (event_id TEXT NOT NULL);
		CREATE TABLE packet_issuance_feed_actions (event_id TEXT NOT NULL);
		INSERT INTO events(id) VALUES ('clean'),('dirty');
		INSERT INTO packet_issuance_registrations(registration_id,event_id,value_json)
			VALUES ('clean-row','clean','{"id":"clean-row"}'),('dirty-row','dirty','{"id":"dirty-row"}');
		INSERT INTO packet_issuance_operations(event_id) VALUES ('dirty')`); err != nil {
		t.Fatal(err)
	}
	if err := addPacketIssuanceSnapshotRebase(db); err != nil {
		t.Fatal(err)
	}
	var clean, dirty int
	if err := db.QueryRow(`SELECT COUNT(*) FROM packet_issuance_site_registrations WHERE event_id='clean'`).Scan(&clean); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM packet_issuance_site_registrations WHERE event_id='dirty'`).Scan(&dirty); err != nil {
		t.Fatal(err)
	}
	if clean != 1 || dirty != 0 {
		t.Fatalf("clean=%d dirty=%d", clean, dirty)
	}
}

func TestPacketFeedRetentionMigrationAddsFloorAndArchiveIdempotently(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "packet-retention.chrono"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		PRAGMA foreign_keys=ON;
		CREATE TABLE events (id TEXT PRIMARY KEY);
		CREATE TABLE packet_issuance_feed_heads (event_id TEXT PRIMARY KEY REFERENCES events(id),last_sequence INTEGER NOT NULL DEFAULT 0);
		INSERT INTO events(id) VALUES ('event');
		INSERT INTO packet_issuance_feed_heads(event_id,last_sequence) VALUES ('event',7)`); err != nil {
		t.Fatal(err)
	}
	if err := addPacketIssuanceFeedRetention(db); err != nil {
		t.Fatal(err)
	}
	if err := addPacketIssuanceFeedRetention(db); err != nil {
		t.Fatalf("second migration: %v", err)
	}
	var floor int64
	if err := db.QueryRow(`SELECT first_available_sequence FROM packet_issuance_feed_heads WHERE event_id='event'`).Scan(&floor); err != nil {
		t.Fatal(err)
	}
	var archive int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='packet_issuance_feed_archives'`).Scan(&archive); err != nil {
		t.Fatal(err)
	}
	if floor != 1 || archive != 1 {
		t.Fatalf("floor=%d archive=%d", floor, archive)
	}
}
