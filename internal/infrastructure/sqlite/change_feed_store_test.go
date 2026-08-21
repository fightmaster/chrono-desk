package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

func TestMigrationAddsPullCursorToExistingSyncConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.chrono")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE sync_config (
		event_id TEXT PRIMARY KEY, base_url TEXT NOT NULL DEFAULT '', token TEXT NOT NULL DEFAULT '',
		last_synced_at INTEGER, last_payload_hash TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := New(db); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"pull_cursor", "last_pulled_at", "projection_pending"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('sync_config') WHERE name = ?`, column).Scan(&count); err != nil || count != 1 {
			t.Fatalf("column %s count=%d err=%v", column, count, err)
		}
	}
}

func TestApplyObservationFeedPageCommitsRowsAndCursorAtomically(t *testing.T) {
	store := newChangeFeedStore(t)
	ctx := context.Background()
	logs := []domain.RfidLog{
		{ID: "remote-1", EventID: "ev1", TimeMs: 1000, Board: "start", ObservationVersion: 1, CaptureSourceID: "stopwatch:start", OriginSystem: "stopwatch", OriginInstanceID: "watch-1", OriginSequence: 1},
		{ID: "remote-2", EventID: "ev1", TimeMs: 2000, Board: "split", ObservationVersion: 1, CaptureSourceID: "stopwatch:split", OriginSystem: "stopwatch", OriginInstanceID: "watch-2", OriginSequence: 9},
	}

	if err := store.ApplyObservationFeedPage(ctx, "ev1", logs, "cursor-2", 1234); err != nil {
		t.Fatal(err)
	}
	cursor, err := store.GetPullCursor(ctx, "ev1")
	if err != nil || cursor == nil || *cursor != "cursor-2" {
		t.Fatalf("cursor = %v, err = %v", cursor, err)
	}
	if count, err := store.CountRfidLogs(ctx, "ev1"); err != nil || count != 2 {
		t.Fatalf("rfid count = %d, err = %v", count, err)
	}
	var outbox int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM observation_outbox`).Scan(&outbox); err != nil || outbox != 0 {
		t.Fatalf("outbox = %d, err = %v", outbox, err)
	}
}

func TestApplyObservationFeedPageRejectsRawConflictAndKeepsPreviousCursor(t *testing.T) {
	store := newChangeFeedStore(t)
	ctx := context.Background()
	original := domain.RfidLog{ID: "same-id", EventID: "ev1", TimeMs: 1000, Board: "start"}
	if err := store.ApplyObservationFeedPage(ctx, "ev1", []domain.RfidLog{original}, "cursor-1", 1000); err != nil {
		t.Fatal(err)
	}
	conflict := original
	conflict.TimeMs = 9999
	if err := store.ApplyObservationFeedPage(ctx, "ev1", []domain.RfidLog{conflict}, "cursor-2", 2000); err == nil {
		t.Fatal("expected immutable raw conflict")
	}
	cursor, err := store.GetPullCursor(ctx, "ev1")
	if err != nil || cursor == nil || *cursor != "cursor-1" {
		t.Fatalf("cursor advanced after conflict: %v, err = %v", cursor, err)
	}
	var timeMs int64
	if err := store.DB().QueryRow(`SELECT time_ms FROM rfid_logs WHERE id = 'same-id'`).Scan(&timeMs); err != nil || timeMs != 1000 {
		t.Fatalf("stored time = %d, err = %v", timeMs, err)
	}
}

func TestApplyObservationFeedPageRollsBackEarlierRowsWhenLastWriteFails(t *testing.T) {
	store := newChangeFeedStore(t)
	ctx := context.Background()
	if _, err := store.DB().Exec(`CREATE TRIGGER reject_bad_feed BEFORE INSERT ON rfid_logs
		WHEN NEW.id = 'bad' BEGIN SELECT RAISE(ABORT, 'bad feed row'); END`); err != nil {
		t.Fatal(err)
	}
	err := store.ApplyObservationFeedPage(ctx, "ev1", []domain.RfidLog{
		{ID: "good", EventID: "ev1", TimeMs: 1000, Board: "start"},
		{ID: "bad", EventID: "ev1", TimeMs: 2000, Board: "finish"},
	}, "cursor-bad", 2000)
	if err == nil {
		t.Fatal("expected feed page failure")
	}
	if count, err := store.CountRfidLogs(ctx, "ev1"); err != nil || count != 0 {
		t.Fatalf("partial page persisted: count=%d err=%v", count, err)
	}
	if cursor, err := store.GetPullCursor(ctx, "ev1"); err != nil || cursor != nil {
		t.Fatalf("cursor persisted after rollback: %v err=%v", cursor, err)
	}
}

func TestApplyObservationFeedPageFillsLegacyOriginAndAppliesState(t *testing.T) {
	store := newChangeFeedStore(t)
	ctx := context.Background()
	legacy := domain.RfidLog{ID: "overlap", EventID: "ev1", TimeMs: 1000, Board: "split"}
	if _, err := store.InsertRfidLogs(ctx, []domain.RfidLog{legacy}); err != nil {
		t.Fatal(err)
	}
	disabledAt := int64(1_780_000_000_000)
	remote := legacy
	remote.DisabledAt = &disabledAt
	remote.ObservationVersion = 1
	remote.CaptureSourceID = "stopwatch:split"
	remote.OriginSystem = "stopwatch"
	remote.OriginInstanceID = "watch-1"
	remote.OriginSequence = 7
	if err := store.ApplyObservationFeedPage(ctx, "ev1", []domain.RfidLog{remote}, "cursor-state", 2000); err != nil {
		t.Fatal(err)
	}
	var gotDisabled, gotSequence int64
	var gotOrigin string
	if err := store.DB().QueryRow(`SELECT disabled_at, origin_instance_id, origin_sequence FROM rfid_logs WHERE id = 'overlap'`).
		Scan(&gotDisabled, &gotOrigin, &gotSequence); err != nil {
		t.Fatal(err)
	}
	if gotDisabled != disabledAt || gotOrigin != "watch-1" || gotSequence != 7 {
		t.Fatalf("updated overlap = disabled:%d origin:%q sequence:%d", gotDisabled, gotOrigin, gotSequence)
	}
}

func TestApplyObservationFeedPageClassifiesCommittedMutations(t *testing.T) {
	store := newChangeFeedStore(t)
	ctx := context.Background()
	base := domain.RfidLog{ID: "remote", EventID: "ev1", TimeMs: 1000, Board: "split"}

	mutations, err := store.ApplyObservationFeedPageWithMutations(ctx, "ev1", []domain.RfidLog{base}, "cursor-1", 1000)
	if err != nil || len(mutations) != 1 || mutations[0].Kind != ObservationFeedInserted {
		t.Fatalf("insert mutations=%+v err=%v", mutations, err)
	}
	if pending, err := store.ProjectionPending(ctx, "ev1"); err != nil || !pending {
		t.Fatalf("projection pending=%v err=%v after insert", pending, err)
	}

	enriched := base
	enriched.ObservationVersion = 1
	enriched.OriginSystem = "stopwatch"
	mutations, err = store.ApplyObservationFeedPageWithMutations(ctx, "ev1", []domain.RfidLog{enriched}, "cursor-2", 2000)
	if err != nil || len(mutations) != 1 || mutations[0].Kind != ObservationFeedDuplicate {
		t.Fatalf("metadata-only mutations=%+v err=%v", mutations, err)
	}

	disabledAt := int64(3000)
	disabled := enriched
	disabled.DisabledAt = &disabledAt
	mutations, err = store.ApplyObservationFeedPageWithMutations(ctx, "ev1", []domain.RfidLog{disabled}, "cursor-3", 3000)
	if err != nil || len(mutations) != 1 || mutations[0].Kind != ObservationFeedStateChanged {
		t.Fatalf("state mutations=%+v err=%v", mutations, err)
	}

	mutations, err = store.ApplyObservationFeedPageWithMutations(ctx, "ev1", []domain.RfidLog{disabled}, "cursor-4", 4000)
	if err != nil || len(mutations) != 1 || mutations[0].Kind != ObservationFeedDuplicate {
		t.Fatalf("repeated state mutations=%+v err=%v", mutations, err)
	}
	if err := store.ClearProjectionPending(ctx, "ev1"); err != nil {
		t.Fatal(err)
	}
	if pending, err := store.ProjectionPending(ctx, "ev1"); err != nil || pending {
		t.Fatalf("projection pending=%v err=%v after clear", pending, err)
	}
}

func newChangeFeedStore(t *testing.T) *Store {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "event.chrono"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := New(db, WithOriginInstanceID("desk-1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertEvent(context.Background(), domain.Event{ID: "ev1", Name: "Event"}); err != nil {
		t.Fatal(err)
	}
	return store
}
