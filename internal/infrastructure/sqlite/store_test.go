package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "event.chrono"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := New(db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

func TestInsertOwnedRfidLogsJournalsOnlyNewLocalRows(t *testing.T) {
	store := newTestStoreWithOrigin(t, "desk-installation-1")
	ctx := context.Background()
	if err := store.UpsertEvent(ctx, domain.Event{ID: "ev1", Name: "E"}); err != nil {
		t.Fatal(err)
	}

	foreign := domain.RfidLog{ID: "foreign", EventID: "ev1", TimeMs: 1000, Board: "remote-point"}
	if _, err := store.InsertRfidLogs(ctx, []domain.RfidLog{foreign}); err != nil {
		t.Fatal(err)
	}
	local := domain.RfidLog{
		ID: "local", EventID: "ev1", TimeMs: 2000, Board: "Feibot:U659",
		CaptureSourceID: "chrono-desk:ev1:Feibot:U659",
	}
	repeatedForeign := foreign
	repeatedForeign.CaptureSourceID = "chrono-desk:ev1:remote-point"
	inserted, err := store.InsertOwnedRfidLogs(ctx, []domain.RfidLog{repeatedForeign, local})
	if err != nil {
		t.Fatal(err)
	}
	if inserted != 1 {
		t.Fatalf("inserted = %d, want 1", inserted)
	}

	var version, sequence int64
	var captureSource, originSystem, originInstance string
	if err := store.DB().QueryRow(`
		SELECT observation_version, capture_source_id, origin_system, origin_instance_id, origin_sequence
		FROM rfid_logs WHERE id = 'local'`).Scan(
		&version, &captureSource, &originSystem, &originInstance, &sequence,
	); err != nil {
		t.Fatal(err)
	}
	if version != 1 || captureSource != local.CaptureSourceID || originSystem != "chrono-desk" ||
		originInstance != "desk-installation-1" || sequence != 1 {
		t.Errorf("local origin = version:%d source:%q system:%q instance:%q sequence:%d",
			version, captureSource, originSystem, originInstance, sequence)
	}

	var outboxCount int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM observation_outbox`).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if outboxCount != 1 {
		t.Errorf("outbox rows = %d, want 1", outboxCount)
	}
	var foreignVersion sql.NullInt64
	if err := store.DB().QueryRow(`SELECT observation_version FROM rfid_logs WHERE id = 'foreign'`).Scan(&foreignVersion); err != nil {
		t.Fatal(err)
	}
	if foreignVersion.Valid {
		t.Errorf("foreign observation was claimed as local: version=%d", foreignVersion.Int64)
	}
}

func TestInsertOwnedRfidLogsRollsBackLogWhenJournalFails(t *testing.T) {
	store := newTestStoreWithOrigin(t, "desk-installation-1")
	ctx := context.Background()
	if err := store.UpsertEvent(ctx, domain.Event{ID: "ev1", Name: "E"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`
		CREATE TRIGGER fail_observation_outbox BEFORE INSERT ON observation_outbox
		BEGIN SELECT RAISE(FAIL, 'forced outbox failure'); END`); err != nil {
		t.Fatal(err)
	}

	_, err := store.InsertOwnedRfidLogs(ctx, []domain.RfidLog{{
		ID: "local", EventID: "ev1", TimeMs: 2000, Board: "Feibot:U659",
		CaptureSourceID: "chrono-desk:ev1:Feibot:U659",
	}})
	if err == nil {
		t.Fatal("expected journal failure")
	}
	var count int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM rfid_logs WHERE id = 'local'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Error("rfid log survived failed journal insert")
	}
}

func newTestStoreWithOrigin(t *testing.T, originInstanceID string) *Store {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "event.chrono"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := New(db, WithOriginInstanceID(originInstanceID))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store
}

func TestSchemaIsIdempotent(t *testing.T) {
	store := newTestStore(t)
	if _, err := New(store.DB()); err != nil {
		t.Fatalf("second migration run: %v", err)
	}
}

func TestMigrationAddsObservationOriginToExistingRfidLogs(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "legacy.chrono"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE rfid_logs (
			id TEXT PRIMARY KEY, event_id TEXT NOT NULL, status INTEGER NOT NULL DEFAULT 0,
			number INTEGER NOT NULL DEFAULT 0, time_ms INTEGER NOT NULL, ant INTEGER NOT NULL DEFAULT 0,
			epc TEXT NOT NULL DEFAULT '', rssi INTEGER NOT NULL DEFAULT 0, board TEXT NOT NULL, disabled_at INTEGER
		)`); err != nil {
		t.Fatal(err)
	}
	if _, err := New(db); err != nil {
		t.Fatalf("migrate legacy schema: %v", err)
	}
	for _, column := range []string{
		"observation_version", "capture_source_id", "origin_system", "origin_instance_id", "origin_sequence",
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('rfid_logs') WHERE name = ?`, column).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Errorf("column %s count = %d, want 1", column, count)
		}
	}
	var outboxCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='observation_outbox'`).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if outboxCount != 1 {
		t.Error("observation_outbox was not created")
	}
}

func TestUpsertAndGetEvent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	e := domain.Event{
		ID:                "ev1",
		Name:              "Test Marathon",
		Slug:              "test-marathon",
		Date:              "2026-06-05",
		UseRaceDateForAge: true,
	}
	if err := store.UpsertEvent(ctx, e); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Upsert again with changed name must update, not duplicate.
	e.Name = "Renamed Marathon"
	if err := store.UpsertEvent(ctx, e); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}

	got, err := store.GetEvent(ctx, "ev1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "Renamed Marathon" {
		t.Errorf("name = %q, want %q", got.Name, "Renamed Marathon")
	}
	if !got.UseRaceDateForAge {
		t.Error("UseRaceDateForAge = false, want true")
	}
}

func TestInsertRfidLogsIsIdempotent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if err := store.UpsertEvent(ctx, domain.Event{ID: "ev1", Name: "E"}); err != nil {
		t.Fatalf("upsert event: %v", err)
	}

	logs := []domain.RfidLog{
		{ID: "aaa", EventID: "ev1", TimeMs: 1000, Ant: 1, EPC: "E280ABC", Board: "Feibot:U659"},
		{ID: "bbb", EventID: "ev1", TimeMs: 2000, Ant: 1, EPC: "E280DEF", Board: "Feibot:U659"},
	}
	inserted, err := store.InsertRfidLogs(ctx, logs)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if inserted != 2 {
		t.Errorf("inserted = %d, want 2", inserted)
	}

	// Re-import of the same flash drive: same ids, plus one new log.
	logs = append(logs, domain.RfidLog{ID: "ccc", EventID: "ev1", TimeMs: 3000, Ant: 2, EPC: "E280ABC", Board: "Feibot:U659"})
	inserted, err = store.InsertRfidLogs(ctx, logs)
	if err != nil {
		t.Fatalf("re-insert: %v", err)
	}
	if inserted != 1 {
		t.Errorf("inserted on re-import = %d, want 1 (duplicates ignored)", inserted)
	}

	total, err := store.CountRfidLogs(ctx, "ev1")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
}
