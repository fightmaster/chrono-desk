package service

import (
	"context"
	"encoding/json"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

func failJournalWrites(t *testing.T, store *sqlite.Store) {
	t.Helper()
	_, err := store.DB().Exec(`
		CREATE TEMP TRIGGER fail_local_change
		BEFORE INSERT ON local_changes
		BEGIN
			SELECT RAISE(FAIL, 'forced journal failure');
		END`)
	if err != nil {
		t.Fatalf("create journal failure trigger: %v", err)
	}
}

func failMemberJournalWrites(t *testing.T, store *sqlite.Store) {
	t.Helper()
	_, err := store.DB().Exec(`
		CREATE TEMP TRIGGER fail_member_local_change
		BEFORE INSERT ON local_changes
		WHEN NEW.entity = 'member'
		BEGIN
			SELECT RAISE(FAIL, 'forced member journal failure');
		END`)
	if err != nil {
		t.Fatalf("create member journal failure trigger: %v", err)
	}
}

func TestApplyEditRollsBackWhenJournalFails(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	importFixture(t, store)
	failJournalWrites(t, store)

	before, err := store.GetRace(ctx, "race-10k")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyEdit(ctx, store, EditRequest{
		Entity: "race", EntityID: before.ID, Field: "name", Value: json.RawMessage(`"changed"`),
	}); err == nil {
		t.Fatal("edit must fail when the journal rejects the entry")
	}
	after, err := store.GetRace(ctx, before.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Name != before.Name {
		t.Fatalf("race name = %q, want rollback to %q", after.Name, before.Name)
	}
}

func TestRaceStartShiftRollsBackAllChangesWhenMemberJournalFails(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	importFixture(t, store)

	const oldStart = int64(1_780_812_000_000)
	if _, err := store.DB().ExecContext(ctx,
		`UPDATE members SET start_time_ms = ? WHERE id IN ('mem-1', 'mem-2')`, oldStart); err != nil {
		t.Fatal(err)
	}
	failMemberJournalWrites(t, store)

	if _, err := ApplyEdit(ctx, store, EditRequest{
		Entity: "race", EntityID: "race-10k", Field: "started_at_ms",
		Value: json.RawMessage(`1780812120000`),
	}); err == nil {
		t.Fatal("race start edit must fail when a shifted-member journal entry fails")
	}
	race, err := store.GetRace(ctx, "race-10k")
	if err != nil {
		t.Fatal(err)
	}
	if race.StartedAtMs == nil || *race.StartedAtMs != oldStart {
		t.Fatalf("race start = %v, want rollback to %d", race.StartedAtMs, oldStart)
	}
	for _, memberID := range []string{"mem-1", "mem-2"} {
		member, err := store.GetMember(ctx, memberID)
		if err != nil {
			t.Fatal(err)
		}
		if member.StartTimeMs == nil || *member.StartTimeMs != oldStart {
			t.Fatalf("member %s start = %v, want rollback to %d", memberID, member.StartTimeMs, oldStart)
		}
	}
	changes, err := store.ListLocalChanges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("journal entries = %d, want rollback to zero", len(changes))
	}
}

func TestCreateMemberRollsBackWhenJournalFails(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	importFixture(t, store)
	failJournalWrites(t, store)

	before, err := store.ListMembersByEvent(ctx, "ev-100")
	if err != nil {
		t.Fatal(err)
	}
	number := int64(909)
	if _, _, err := CreateMember(ctx, store, "ev-100", CreateMemberRequest{
		RaceID: "race-10k", FirstName: "Atomic", LastName: "Member",
		Number: &number, DOB: sptr("2000-01-01"),
	}); err == nil {
		t.Fatal("member creation must fail when the journal rejects the entry")
	}
	after, err := store.ListMembersByEvent(ctx, "ev-100")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("member count = %d, want rollback to %d", len(after), len(before))
	}
}

func TestManualFinishRollsBackWhenJournalFails(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	importFixture(t, store)
	failJournalWrites(t, store)

	if _, err := ManualFinishClean(ctx, store, "ev-100", "mem-2", 600_000); err == nil {
		t.Fatal("manual finish must fail when the journal rejects the entry")
	}
	member, err := store.GetMember(ctx, "mem-2")
	if err != nil {
		t.Fatal(err)
	}
	if member.StartTimeMs != nil || member.FinishTimeMs != nil || member.CleanTime != nil {
		t.Fatalf("member times survived rollback: start=%v finish=%v clean=%v",
			member.StartTimeMs, member.FinishTimeMs, member.CleanTime)
	}
	manual, err := store.ListManualResultsForMember(ctx, "ev-100", "mem-2")
	if err != nil {
		t.Fatal(err)
	}
	if len(manual) != 0 {
		t.Fatalf("manual results = %d, want rollback to zero", len(manual))
	}
}

func TestCheckpointMutationsRollBackWhenJournalFails(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		store := newTestStore(t)
		ctx := context.Background()
		importFixture(t, store)
		failJournalWrites(t, store)

		if _, _, err := CreateCheckpoint(ctx, store, "ev-100", CreateCheckpointRequest{
			RaceID: "race-10k", Name: "Atomic", Type: 2, Sort: 20, Board: "Feibot:atomic",
		}); err == nil {
			t.Fatal("checkpoint creation must fail when the journal rejects the entry")
		}
		checkpoints, err := store.ListCheckpointsByEvent(ctx, "ev-100")
		if err != nil {
			t.Fatal(err)
		}
		for _, checkpoint := range checkpoints {
			if checkpoint.Name == "Atomic" {
				t.Fatal("created checkpoint survived rollback")
			}
		}
	})

	t.Run("delete", func(t *testing.T) {
		store := newTestStore(t)
		ctx := context.Background()
		importFixture(t, store)
		failJournalWrites(t, store)

		if _, err := DeleteCheckpoint(ctx, store, "ev-100", "cp-finish"); err == nil {
			t.Fatal("checkpoint deletion must fail when the journal rejects the entry")
		}
		if _, err := store.GetCheckpoint(ctx, "cp-finish"); err != nil {
			t.Fatalf("deleted checkpoint was not rolled back: %v", err)
		}
	})
}
