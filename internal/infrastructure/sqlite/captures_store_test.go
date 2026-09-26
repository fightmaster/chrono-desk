package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

func TestPendingCaptures(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.UpsertEvent(ctx, domain.Event{ID: "ev1", Name: "E"}); err != nil {
		t.Fatal(err)
	}

	id1, err := store.CreatePendingCapture(ctx, "ev1", 1000)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.CreatePendingCapture(ctx, "ev1", 2000); err != nil {
		t.Fatalf("create: %v", err)
	}

	caps, err := store.ListPendingCaptures(ctx, "ev1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(caps) != 2 {
		t.Fatalf("len = %d, want 2", len(caps))
	}
	if caps[0].TimeMs != 2000 { // newest first (id DESC)
		t.Errorf("first capture time = %d, want 2000", caps[0].TimeMs)
	}

	if err := store.DeletePendingCapture(ctx, "ev1", id1); err != nil {
		t.Fatalf("delete: %v", err)
	}
	caps, _ = store.ListPendingCaptures(ctx, "ev1")
	if len(caps) != 1 || caps[0].TimeMs != 2000 {
		t.Fatalf("after delete = %+v, want one capture at 2000", caps)
	}
}

func TestLastCaptureNumberSurvivesRemovalAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "event.chrono")
	openStore := func() *Store {
		t.Helper()
		db, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		store, err := New(db)
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
		return store
	}
	store := openStore()
	if err := store.UpsertEvent(ctx, domain.Event{ID: "ev1", Name: "E"}); err != nil {
		t.Fatal(err)
	}
	assertLast := func(want int64) {
		t.Helper()
		got, err := store.LastCaptureNumber(ctx)
		if err != nil || got != want {
			t.Fatalf("last capture number = %d, %v; want %d", got, err, want)
		}
	}
	assertLast(0)
	first, err := store.CreatePendingCapture(ctx, "ev1", 1000)
	if err != nil || first != 1 {
		t.Fatalf("first capture = %d, %v; want 1", first, err)
	}
	second, err := store.CreatePendingCapture(ctx, "ev1", 2000)
	if err != nil || second != 2 {
		t.Fatalf("second capture = %d, %v; want 2", second, err)
	}
	if err := store.DeletePendingCapture(ctx, "ev1", second); err != nil {
		t.Fatal(err)
	}
	if err := store.DeletePendingCapture(ctx, "ev1", first); err != nil {
		t.Fatal(err)
	}
	assertLast(2)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openStore()
	t.Cleanup(func() { store.Close() })
	assertLast(2)
	third, err := store.CreatePendingCapture(ctx, "ev1", 3000)
	if err != nil || third != 3 {
		t.Fatalf("third capture = %d, %v; want 3", third, err)
	}
	assertLast(3)
}

func TestCountRacesAndMembers(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.UpsertEvent(ctx, domain.Event{ID: "ev1", Name: "E"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRace(ctx, domain.Race{ID: "r1", EventID: "ev1", Name: "10k", Format: domain.FormatFixedDistance}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"m1", "m2"} {
		if err := store.UpsertMember(ctx, domain.Member{ID: id, EventID: "ev1", RaceID: "r1", FirstName: "A", LastName: id}); err != nil {
			t.Fatal(err)
		}
	}

	races, members, err := store.CountRacesAndMembers(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if races != 1 || members != 2 {
		t.Fatalf("races=%d members=%d, want 1/2", races, members)
	}
}
