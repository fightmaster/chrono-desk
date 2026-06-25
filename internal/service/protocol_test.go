package service

import (
	"context"
	"io"
	"log"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

// The protocol header counts are authoritative: total = start list, started =
// total − DNS, finished = members with a place. The fixture has mem-1 (OK, chip
// finish) and mem-2 (DNS, no finish).
func TestBuildProtocolCounts(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	importFixture(t, store)
	if _, err := NewRecounter(store, log.New(io.Discard, "", 0), false).Recount(ctx, "ev-100", ""); err != nil {
		t.Fatalf("recount: %v", err)
	}

	p, err := BuildProtocol(ctx, store, "race-10k")
	if err != nil {
		t.Fatal(err)
	}
	if p.Counts.Total != 2 {
		t.Errorf("total = %d, want 2", p.Counts.Total)
	}
	if p.Counts.DNS != 1 {
		t.Errorf("dns = %d, want 1 (mem-2)", p.Counts.DNS)
	}
	if p.Counts.Started != 1 {
		t.Errorf("started = %d, want 1 (total − DNS)", p.Counts.Started)
	}
	if p.Counts.Finished != 1 {
		t.Errorf("finished = %d, want 1 (mem-1 has a place)", p.Counts.Finished)
	}
}

// Fail closed: an unsupported race format must error instead of silently
// ranking as FixedDistance and emitting a plausible-but-wrong protocol.
func TestBuildProtocolRejectsUnsupportedFormat(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if err := store.UpsertEvent(ctx, domain.Event{ID: "ev1", Name: "E"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRace(ctx, domain.Race{
		ID: "r-stopwatch", EventID: "ev1", Name: "Секундомер", Format: domain.FormatRun5Stopwatch,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildProtocol(ctx, store, "r-stopwatch"); err == nil {
		t.Fatal("expected an error for an unsupported race format")
	}

	// A supported format still builds.
	if err := store.UpsertRace(ctx, domain.Race{
		ID: "r-fixed", EventID: "ev1", Name: "10 км", Format: domain.FormatFixedDistance,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildProtocol(ctx, store, "r-fixed"); err != nil {
		t.Fatalf("FixedDistance must build: %v", err)
	}
}
