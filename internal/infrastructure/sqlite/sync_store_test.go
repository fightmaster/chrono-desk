package sqlite

import (
	"context"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

func TestObservationBatchRetriesStableAndNeverIncludesForeignRows(t *testing.T) {
	store := newTestStoreWithOrigin(t, "desk-installation-1")
	ctx := context.Background()
	if err := store.UpsertEvent(ctx, domain.Event{ID: "ev1", Name: "E"}); err != nil {
		t.Fatal(err)
	}
	foreign := domain.RfidLog{ID: "foreign", EventID: "ev1", TimeMs: 900, Board: "remote"}
	if _, err := store.InsertRfidLogs(ctx, []domain.RfidLog{foreign}); err != nil {
		t.Fatal(err)
	}
	owned := []domain.RfidLog{
		{ID: "local-1", EventID: "ev1", TimeMs: 1000, Board: "B1", CaptureSourceID: "chrono-desk:ev1:B1"},
		{ID: "local-2", EventID: "ev1", TimeMs: 2000, Board: "B1", CaptureSourceID: "chrono-desk:ev1:B1"},
	}
	if _, err := store.InsertOwnedRfidLogs(ctx, owned); err != nil {
		t.Fatal(err)
	}

	batch, err := store.PrepareObservationBatch(ctx, "ev1", 20_000, time.UnixMilli(3000))
	if err != nil {
		t.Fatal(err)
	}
	if batch == nil || len(batch.Items) != 2 || batch.Items[0].ID != "local-1" || batch.Items[1].ID != "local-2" {
		t.Fatalf("first batch = %+v, want two owned rows", batch)
	}

	if _, err := store.InsertOwnedRfidLogs(ctx, []domain.RfidLog{{
		ID: "local-3", EventID: "ev1", TimeMs: 3000, Board: "B1", CaptureSourceID: "chrono-desk:ev1:B1",
	}}); err != nil {
		t.Fatal(err)
	}
	retry, err := store.PrepareObservationBatch(ctx, "ev1", 20_000, time.UnixMilli(4000))
	if err != nil {
		t.Fatal(err)
	}
	if retry.BatchID != batch.BatchID || len(retry.Items) != 2 {
		t.Fatalf("retry batch changed: first=%+v retry=%+v", batch, retry)
	}

	acks := []ObservationOutboxAck{
		{ObservationID: "local-1", OriginSequence: 1, Status: "inserted"},
		{ObservationID: "local-2", OriginSequence: 2, Status: "duplicate"},
	}
	if err := store.ApplyObservationAck(ctx, batch.BatchID, acks, time.UnixMilli(5000)); err != nil {
		t.Fatal(err)
	}
	next, err := store.PrepareObservationBatch(ctx, "ev1", 20_000, time.UnixMilli(6000))
	if err != nil {
		t.Fatal(err)
	}
	if next == nil || len(next.Items) != 1 || next.Items[0].ID != "local-3" {
		t.Fatalf("next batch = %+v, want only newly pending row", next)
	}

	var foreignOutbox int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM observation_outbox WHERE observation_id = 'foreign'`).Scan(&foreignOutbox); err != nil {
		t.Fatal(err)
	}
	if foreignOutbox != 0 {
		t.Error("foreign row entered outbound journal")
	}
}

func TestObservationAckMismatchRollsBackAllStates(t *testing.T) {
	store := newTestStoreWithOrigin(t, "desk-installation-1")
	ctx := context.Background()
	if err := store.UpsertEvent(ctx, domain.Event{ID: "ev1", Name: "E"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertOwnedRfidLogs(ctx, []domain.RfidLog{{
		ID: "local-1", EventID: "ev1", TimeMs: 1000, Board: "B1", CaptureSourceID: "chrono-desk:ev1:B1",
	}}); err != nil {
		t.Fatal(err)
	}
	batch, err := store.PrepareObservationBatch(ctx, "ev1", 20_000, time.UnixMilli(2000))
	if err != nil {
		t.Fatal(err)
	}
	err = store.ApplyObservationAck(ctx, batch.BatchID, []ObservationOutboxAck{{
		ObservationID: "wrong-id", OriginSequence: 1, Status: "inserted",
	}}, time.UnixMilli(3000))
	if err == nil {
		t.Fatal("expected mismatched acknowledgement to fail")
	}
	var state string
	if err := store.DB().QueryRow(`SELECT state FROM observation_outbox WHERE observation_id = 'local-1'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "sent" {
		t.Fatalf("state after failed ack = %q, want sent for safe retry", state)
	}
}
