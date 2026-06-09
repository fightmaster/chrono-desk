package sqlite

import (
	"context"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

// ListRecentPasses must surface manual judge finishes inline in the feed
// (Manual=true, ResultID set) alongside chip reads, ordered by time, so the
// live screen can show and delete them without a separate list.
func TestListRecentPassesIncludesManual(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if err := store.UpsertEvent(ctx, domain.Event{ID: "ev1", Name: "E"}); err != nil {
		t.Fatalf("upsert event: %v", err)
	}
	started := int64(0)
	if err := store.UpsertRace(ctx, domain.Race{ID: "r1", EventID: "ev1", Name: "R", StartedAtMs: &started, Format: domain.FormatFixedDistance}); err != nil {
		t.Fatalf("upsert race: %v", err)
	}
	epc := "E280ABC"
	num := int64(7)
	if err := store.UpsertMember(ctx, domain.Member{
		ID: "m1", EventID: "ev1", RaceID: "r1", Number: &num, EPC: &epc,
		FirstName: "Ivan", LastName: "Petrov",
	}); err != nil {
		t.Fatalf("upsert member: %v", err)
	}

	// A chip read at t=2000 and a later manual finish at t=5000.
	if _, err := store.InsertRfidLogs(ctx, []domain.RfidLog{
		{ID: "log1", EventID: "ev1", TimeMs: 2000, Ant: 1, EPC: epc, Board: "Feibot:U659"},
	}); err != nil {
		t.Fatalf("insert log: %v", err)
	}
	resultID, err := store.InsertManualResult(ctx, "ev1", "r1", "m1", 5000, &num)
	if err != nil {
		t.Fatalf("insert manual: %v", err)
	}

	feed, err := store.ListRecentPasses(ctx, "ev1", 50)
	if err != nil {
		t.Fatalf("list recent passes: %v", err)
	}
	if len(feed) != 2 {
		t.Fatalf("feed length = %d, want 2 (chip read + manual)", len(feed))
	}

	// Newest first: the manual finish (t=5000) leads the chip read (t=2000).
	manual := feed[0]
	if !manual.Manual {
		t.Errorf("feed[0].Manual = false, want true (manual finish should sort first)")
	}
	if manual.ResultID == nil || *manual.ResultID != resultID {
		t.Errorf("feed[0].ResultID = %v, want %d", manual.ResultID, resultID)
	}
	if manual.MemberID == nil || *manual.MemberID != "m1" {
		t.Errorf("feed[0].MemberID = %v, want m1", manual.MemberID)
	}
	if manual.Number == nil || *manual.Number != num || manual.LastName == nil || *manual.LastName != "Petrov" {
		t.Errorf("feed[0] participant = №%v %v, want №7 Petrov", manual.Number, manual.LastName)
	}

	chip := feed[1]
	if chip.Manual {
		t.Errorf("feed[1].Manual = true, want false (chip read)")
	}
	if chip.ResultID != nil {
		t.Errorf("feed[1].ResultID = %v, want nil (chip read has no manual id)", *chip.ResultID)
	}
	if chip.LogID != "log1" {
		t.Errorf("feed[1].LogID = %q, want log1", chip.LogID)
	}
}
