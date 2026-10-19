package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

func TestPhotoMetadataVersionChangesOnlyForMergeInputsAndRollsBack(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.UpsertEvent(ctx, domain.Event{ID: "event", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	version := func() int64 {
		v, err := store.PhotoMetadataVersion(ctx, "event")
		if err != nil {
			t.Fatal(err)
		}
		return v.Revision
	}
	if got := version(); got != 0 {
		t.Fatalf("initial version=%d", got)
	}
	p := Photo{ID: "p", SourceID: "camera", TimeMs: 1000, Bib: "1", Frames: json.RawMessage(`[]`)}
	if err := store.UpsertPhoto(ctx, "event", p, 1); err != nil {
		t.Fatal(err)
	}
	if got := version(); got != 1 {
		t.Fatalf("insert version=%d", got)
	}
	p.Frames = json.RawMessage(`[{"timestamp_epoch_ms":1000,"url":"frame.jpg"}]`)
	if err := store.UpsertPhoto(ctx, "event", p, 2); err != nil {
		t.Fatal(err)
	}
	if got := version(); got != 1 {
		t.Fatalf("frames/freshness invalidated merge: %d", got)
	}
	p.Bib = "2"
	if err := store.UpsertPhoto(ctx, "event", p, 3); err != nil {
		t.Fatal(err)
	}
	if got := version(); got != 2 {
		t.Fatalf("bib correction not versioned: %d", got)
	}
	failure := errors.New("rollback")
	if err := store.WithinTx(ctx, func(tx *Store) error {
		_, err := tx.db.ExecContext(ctx, `UPDATE photos SET camera_label='other' WHERE id='p'`)
		if err != nil {
			return err
		}
		return failure
	}); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if got := version(); got != 2 {
		t.Fatalf("rolled back correction advanced version: %d", got)
	}
	if _, err := store.DB().Exec(`DELETE FROM photos WHERE id='p'`); err != nil {
		t.Fatal(err)
	}
	if got := version(); got != 3 {
		t.Fatalf("delete not versioned: %d", got)
	}
}
