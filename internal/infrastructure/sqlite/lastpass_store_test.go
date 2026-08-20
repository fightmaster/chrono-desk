package sqlite

import (
	"context"
	"strings"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

func TestLastPassesInWindowLoadsEveryMemberInOneSetQuery(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	limit := int64(60)
	race := domain.Race{ID: "race-1", EventID: "event-1", Name: "Hour", Format: domain.FormatTimeLimited, TimeLimitSeconds: &limit}

	if err := store.UpsertEvent(ctx, domain.Event{ID: race.EventID, Name: "Event"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRace(ctx, race); err != nil {
		t.Fatal(err)
	}
	for _, checkpoint := range []domain.Checkpoint{
		{ID: "cp-1", EventID: race.EventID, RaceID: race.ID, Name: "First", Sort: 1, Board: "B1"},
		{ID: "cp-2", EventID: race.EventID, RaceID: race.ID, Name: "Second", Sort: 2, Board: "B2"},
	} {
		if err := store.UpsertCheckpoint(ctx, checkpoint); err != nil {
			t.Fatal(err)
		}
	}
	starts := map[string]int64{"member-1": 1_000, "member-2": 5_000}
	for memberID, start := range starts {
		if err := store.UpsertMember(ctx, domain.Member{
			ID: memberID, EventID: race.EventID, RaceID: race.ID,
			FirstName: memberID, StartTimeMs: &start,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct {
		memberID, checkpointID string
		timeMs                 int64
	}{
		{"member-1", "cp-1", 900},    // before personal start
		{"member-1", "cp-1", 60_000}, // valid, older id
		{"member-1", "cp-2", 60_000}, // same time, newer id wins
		{"member-1", "cp-1", 61_001}, // after personal window
		{"member-2", "cp-1", 65_000}, // inclusive window end
	} {
		if _, err := store.DB().ExecContext(ctx, `
			INSERT INTO results (event_id, race_id, member_id, checkpoint_id, time_ms)
			VALUES (?, ?, ?, ?, ?)`, race.EventID, race.ID, row.memberID, row.checkpointID, row.timeMs); err != nil {
			t.Fatal(err)
		}
	}

	passes, err := store.LastPassesInWindow(ctx, race)
	if err != nil {
		t.Fatal(err)
	}
	if len(passes) != 2 {
		t.Fatalf("passes = %d, want 2", len(passes))
	}
	if got := passes["member-1"]; got.TimeMs != 60_000 || got.CheckpointName == nil || *got.CheckpointName != "Second" {
		t.Errorf("member-1 pass = %+v", got)
	}
	if got := passes["member-2"]; got.TimeMs != 65_000 {
		t.Errorf("member-2 pass = %+v", got)
	}
}

func TestLastPassesInWindowQueryUsesRaceAndMemberTimeIndexes(t *testing.T) {
	store := newTestStore(t)
	rows, err := store.DB().Query(`EXPLAIN QUERY PLAN `+lastPassesInWindowSQL, "race-1", int64(60_000))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	details := ""
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details += detail + "\n"
	}
	for _, index := range []string{"idx_members_race", "idx_results_member_time"} {
		if !strings.Contains(details, index) {
			t.Errorf("query plan does not use %s:\n%s", index, details)
		}
	}
}
