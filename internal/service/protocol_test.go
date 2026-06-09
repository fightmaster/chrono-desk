package service

import (
	"context"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

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
