package service

import (
	"context"
	"io"
	"log"
	"path/filepath"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

func ptr(v int64) *int64    { return &v }
func sptr(v string) *string { return &v }

// Full integration: START and FINISH checkpoints on separate reader boards, a
// sleep window on FINISH, a disabled log, and a member without a start read
// (backfilled from race start). Mirrors a real course setup.
func TestRecountDerivesResultsAndMemberTimes(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "event.chrono"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	store, err := sqlite.New(db)
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	const (
		startBoard  = "Feibot:U100"            // start-line reader
		finishBoard = "Feibot:U659"            // finish-line reader
		t0          = int64(1_750_000_000_000) // race start, unix ms
	)

	if err := store.UpsertEvent(ctx, domain.Event{ID: "ev1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRace(ctx, domain.Race{
		ID: "r1", EventID: "ev1", Name: "10k", Format: domain.FormatFixedDistance, StartedAtMs: ptr(t0),
	}); err != nil {
		t.Fatal(err)
	}
	for _, cp := range []domain.Checkpoint{
		{ID: "cp-start", EventID: "ev1", RaceID: "r1", Type: domain.CheckpointStart, Sort: 1, Board: startBoard},
		{ID: "cp-finish", EventID: "ev1", RaceID: "r1", Type: domain.CheckpointFinish, Sort: 2, Board: finishBoard,
			SleepAfterPrevSeconds: ptr(60)},
	} {
		if err := store.UpsertCheckpoint(ctx, cp); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []domain.Member{
		{ID: "mA", EventID: "ev1", RaceID: "r1", EPC: sptr("E280AAA"), FirstName: "A"},
		{ID: "mB", EventID: "ev1", RaceID: "r1", EPC: sptr("E280BBB"), FirstName: "B"},
	} {
		if err := store.UpsertMember(ctx, m); err != nil {
			t.Fatal(err)
		}
	}

	logs := []domain.RfidLog{
		// A: start read 10s after race start.
		{ID: "l1", EventID: "ev1", TimeMs: t0 + 10_000, EPC: "E280AAA", Board: startBoard, Ant: 1},
		// A: finish reader sees the tag 1s later — blocked by the 60s sleep.
		{ID: "l2", EventID: "ev1", TimeMs: t0 + 11_000, EPC: "E280AAA", Board: finishBoard, Ant: 2},
		// A: a judge-disabled read must never produce a result.
		{ID: "l3", EventID: "ev1", TimeMs: t0 + 200_000, EPC: "E280AAA", Board: finishBoard, Ant: 2,
			DisabledAt: ptr(t0 + 300_000)},
		// A: real finish.
		{ID: "l4", EventID: "ev1", TimeMs: t0 + 400_000, EPC: "E280AAA", Board: finishBoard, Ant: 2},
		// B: no start read at all — start must backfill from race started_at.
		{ID: "l5", EventID: "ev1", TimeMs: t0 + 500_000, EPC: "E280BBB", Board: finishBoard, Ant: 2},
		// Unknown tag: ignored.
		{ID: "l6", EventID: "ev1", TimeMs: t0 + 510_000, EPC: "DEADBEEF", Board: finishBoard, Ant: 2},
	}
	if err := store.UpsertRfidLogs(ctx, logs); err != nil {
		t.Fatal(err)
	}

	rec := NewRecounter(store, log.New(io.Discard, "", 0), false)

	for run := 1; run <= 2; run++ { // second run proves idempotency
		stats, err := rec.Recount(ctx, "ev1", "")
		if err != nil {
			t.Fatalf("recount #%d: %v", run, err)
		}
		if stats.LogsReplayed != len(logs) {
			t.Fatalf("run #%d: logs replayed = %d, want %d", run, stats.LogsReplayed, len(logs))
		}

		var resultCount int
		if err := store.DB().QueryRow(`SELECT COUNT(*) FROM results`).Scan(&resultCount); err != nil {
			t.Fatal(err)
		}
		// A: start + finish; B: finish. The sleeping read, the disabled read
		// and the unknown tag produce nothing.
		if resultCount != 3 {
			t.Fatalf("run #%d: results = %d, want 3", run, resultCount)
		}

		assertMemberTimes(t, store, "mA", run, t0+10_000, t0+400_000, "00:06:30")
		assertMemberTimes(t, store, "mB", run, t0, t0+500_000, "00:08:20")
	}
}

func assertMemberTimes(t *testing.T, store *sqlite.Store, memberID string, run int, wantStart, wantFinish int64, wantClean string) {
	t.Helper()
	var start, finish *int64
	var clean *string
	err := store.DB().QueryRow(
		`SELECT start_time_ms, finish_time_ms, clean_time FROM members WHERE id = ?`, memberID).
		Scan(&start, &finish, &clean)
	if err != nil {
		t.Fatalf("member %s: %v", memberID, err)
	}
	if start == nil || *start != wantStart {
		t.Errorf("run #%d %s: start = %v, want %d", run, memberID, start, wantStart)
	}
	if finish == nil || *finish != wantFinish {
		t.Errorf("run #%d %s: finish = %v, want %d", run, memberID, finish, wantFinish)
	}
	if clean == nil || *clean != wantClean {
		t.Errorf("run #%d %s: clean = %v, want %q", run, memberID, clean, wantClean)
	}
}
