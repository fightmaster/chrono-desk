package service

import (
	"context"
	"io"
	"log"
	"strings"
	"testing"
)

// Checkpoint create/delete with the local-wins re-import policy: a judge's
// deletion must survive a fresh site export, a local checkpoint must derive
// results.
func TestCheckpointCreateAndDelete(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	importFixture(t, store)
	rec := NewRecounter(store, log.New(io.Discard, "", 0), false)

	// Baseline: fixture log gives mem-1 a finish at cp-finish.
	if _, err := rec.Recount(ctx, "ev-100", ""); err != nil {
		t.Fatal(err)
	}
	var finish *int64
	if err := store.DB().QueryRow(`SELECT finish_time_ms FROM members WHERE id='mem-1'`).Scan(&finish); err != nil {
		t.Fatal(err)
	}
	if finish == nil {
		t.Fatal("baseline: mem-1 must finish")
	}

	// Judge deletes the finish checkpoint → its results vanish, recount
	// leaves the member without a finish.
	res, err := DeleteCheckpoint(ctx, store, "ev-100", "cp-finish")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !res.RecountNeeded {
		t.Error("deletion must demand a recount")
	}
	if _, err := rec.Recount(ctx, "ev-100", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow(`SELECT finish_time_ms FROM members WHERE id='mem-1'`).Scan(&finish); err != nil {
		t.Fatal(err)
	}
	if finish != nil {
		t.Error("after checkpoint deletion the finish must be gone")
	}

	// A re-import resurrects the site checkpoint — the journal re-deletes it.
	importFixture(t, store)
	var cpCount int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM checkpoints WHERE id='cp-finish'`).Scan(&cpCount); err != nil {
		t.Fatal(err)
	}
	if cpCount != 0 {
		t.Error("deleted checkpoint must stay deleted after re-import (local wins)")
	}

	// A locally created replacement derives results again.
	cpID, res2, err := CreateCheckpoint(ctx, store, "ev-100", CreateCheckpointRequest{
		RaceID: "race-10k", Name: "Финиш (новый)", Type: 3, Sort: 5, Board: "Feibot:U659",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.HasPrefix(cpID, "local-") || !res2.RecountNeeded {
		t.Fatalf("create result: id=%s recount=%v", cpID, res2.RecountNeeded)
	}
	if _, err := rec.Recount(ctx, "ev-100", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow(`SELECT finish_time_ms FROM members WHERE id='mem-1'`).Scan(&finish); err != nil {
		t.Fatal(err)
	}
	if finish == nil {
		t.Error("local checkpoint must derive the finish again")
	}
}
