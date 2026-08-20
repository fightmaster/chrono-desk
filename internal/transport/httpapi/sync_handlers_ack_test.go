package httpapi

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/service"
)

func TestApplyObservationAckPersistsOnlyCompleteMatchingWatermark(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "event.chrono"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := sqlite.New(db, sqlite.WithOriginInstanceID("desk-1"))
	if err != nil {
		t.Fatal(err)
	}
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
	wrongWatermark := int64(2)
	err = applyObservationAck(ctx, store, batch, &service.ObservationAck{
		BatchID: batch.BatchID, OriginInstanceID: batch.OriginInstanceID,
		AcceptedThroughSequence: &wrongWatermark,
		Items:                   []service.ObservationAckItem{{ID: "local-1", OriginSequence: 1, Status: "inserted"}},
	})
	if err == nil {
		t.Fatal("expected wrong watermark to fail")
	}

	correctWatermark := int64(1)
	if err := applyObservationAck(ctx, store, batch, &service.ObservationAck{
		BatchID: batch.BatchID, OriginInstanceID: batch.OriginInstanceID,
		AcceptedThroughSequence: &correctWatermark,
		Items:                   []service.ObservationAckItem{{ID: "local-1", OriginSequence: 1, Status: "inserted"}},
	}); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := store.DB().QueryRow(`SELECT state FROM observation_outbox WHERE observation_id = 'local-1'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "acked" {
		t.Fatalf("outbox state = %q, want acked", state)
	}
}
