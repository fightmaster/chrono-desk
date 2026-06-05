package sqlite

import (
	"context"
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

func TestSchemaIsIdempotent(t *testing.T) {
	store := newTestStore(t)
	if _, err := New(store.DB()); err != nil {
		t.Fatalf("second migration run: %v", err)
	}
}

func TestUpsertAndGetEvent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	e := domain.Event{ID: "ev1", Name: "Test Marathon", Slug: "test-marathon", Date: "2026-06-05"}
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
