package service

import (
	"context"
	"encoding/json"
	"testing"
)

const raceStartMs = int64(1780812000000) // 2026-06-07T09:00:00+03:00 (fixture)

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// Clean-time entry for a chip-less finisher: with no START read the member has
// no start_time_ms, so the finish is computed from the race start (matching the
// processor's fallback) and the clean time equals what the judge typed.
func TestManualFinishCleanUsesRaceStart(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	importFixture(t, store)

	// 00:47:13.250 elapsed.
	cleanMs := int64((47*60+13)*1000 + 250)
	res, err := ManualFinishClean(ctx, store, "ev-100", "mem-2", cleanMs)
	if err != nil {
		t.Fatalf("manual clean: %v", err)
	}
	if res.ResultID == 0 {
		t.Error("manual finish must return a result id for undo")
	}

	var finish *int64
	var clean *string
	if err := store.DB().QueryRow(
		`SELECT finish_time_ms, clean_time FROM members WHERE id = ?`, "mem-2").
		Scan(&finish, &clean); err != nil {
		t.Fatal(err)
	}
	wantFinish := raceStartMs + cleanMs
	if finish == nil || *finish != wantFinish {
		t.Fatalf("finish = %v, want %d", finish, wantFinish)
	}
	if clean == nil || *clean != "00:47:13.250" {
		t.Errorf("clean_time = %v, want 00:47:13.250", clean)
	}
}

// Without any start reference (no member start, race start cleared) clean-time
// entry cannot be resolved and must fail with a clear message.
func TestManualFinishCleanRequiresStart(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	importFixture(t, store)

	if _, err := ApplyEdit(ctx, store, EditRequest{
		Entity: "race", EntityID: "race-10k", Field: "started_at_ms",
		Value: json.RawMessage(`null`),
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := ManualFinishClean(ctx, store, "ev-100", "mem-2", 60000); err == nil {
		t.Error("clean-time entry without a start reference must be rejected")
	}
}

// A chip-less finisher (status OK, no START read) entered manually must appear
// in the protocol — the ranking drops OK members without a start, so the manual
// flow has to backfill start_time_ms (regression: empty kids'-race protocol).
func TestManualFinishAppearsInProtocol(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	importFixture(t, store)

	num := int64(700)
	id, _, err := CreateMember(ctx, store, "ev-100", CreateMemberRequest{
		RaceID: "race-10k", FirstName: "Без", LastName: "Чипа", Number: &num, DOB: sptr("2015-01-01"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ManualFinishClean(ctx, store, "ev-100", id, 193000); err != nil { // 00:03:13
		t.Fatal(err)
	}

	// start_time_ms backfilled (so ranking includes the finisher).
	var startMs *int64
	if err := store.DB().QueryRow(`SELECT start_time_ms FROM members WHERE id = ?`, id).Scan(&startMs); err != nil {
		t.Fatal(err)
	}
	if startMs == nil || *startMs != raceStartMs {
		t.Errorf("start_time_ms = %v, want backfilled %d", startMs, raceStartMs)
	}

	proto, err := BuildProtocol(ctx, store, "race-10k")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range proto.Rows {
		if r.MemberID == id {
			found = true
			if r.Place == nil {
				t.Error("manual finisher has no place")
			}
			if r.CleanTime == nil || *r.CleanTime != "00:03:13" {
				t.Errorf("clean_time = %v", r.CleanTime)
			}
		}
	}
	if !found {
		t.Fatal("manual finisher missing from protocol")
	}
}

// The detailed list backs the live-screen review/undo table: it carries the
// participant's number and name alongside the manual entry.
func TestListManualFinishesDetailed(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	importFixture(t, store)

	if _, err := ManualFinishClean(ctx, store, "ev-100", "mem-2", 600000); err != nil {
		t.Fatal(err)
	}
	list, err := ListManualFinishes(ctx, store, "ev-100", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("manual results = %d, want 1", len(list))
	}
	r := list[0]
	if r.MemberID != "mem-2" || r.LastName != "Ivanova" || r.Number == nil || *r.Number != 102 {
		t.Errorf("detail = %+v, want mem-2/Ivanova/102", r)
	}
	// FormatCleanTime omits the .mmm suffix when milliseconds are zero.
	if r.CleanTime == nil || *r.CleanTime != "00:10:00" {
		t.Errorf("clean_time = %v, want 00:10:00", deref(r.CleanTime))
	}
}
