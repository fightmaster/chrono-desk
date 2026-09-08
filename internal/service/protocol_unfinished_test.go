package service

import (
	"context"
	"reflect"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/ranking"
)

func TestBuildProtocolUnfinishedIsSeparateFromRankedRows(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	if err := store.UpsertEvent(ctx, domain.Event{ID: "ev", Name: "Rehearsal"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRace(ctx, domain.Race{ID: "race", EventID: "ev", Format: domain.FormatFixedDistance}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertCategory(ctx, domain.Category{ID: "cat", Name: "Ж18-39"}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []domain.Member{
		{ID: "finished", StartTimeMs: ptr(1000), FinishTimeMs: ptr(2000), CleanTime: sptr("00:00:01.000")},
		{ID: "dns", Status: domain.StatusDNS},
		{ID: "dnf", Status: domain.StatusDNF},
		{ID: "dsq", Status: domain.StatusDSQ, FinishTimeMs: ptr(2000)},
		// A finish with incomplete/invalid timing is not a missing finish.
		{ID: "finish-no-start", FinishTimeMs: ptr(2000)},
		{ID: "negative-time", StartTimeMs: ptr(3000), FinishTimeMs: ptr(2000), CleanTime: sptr("invalid")},
	} {
		m.EventID, m.RaceID = "ev", "race"
		if err := store.UpsertMember(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	before, err := BuildProtocol(ctx, store, "race")
	if err != nil {
		t.Fatal(err)
	}
	if before.UnfinishedRows == nil || len(before.UnfinishedRows) != 0 {
		t.Fatalf("empty appendix must be [], got %#v", before.UnfinishedRows)
	}
	for _, m := range []domain.Member{
		{ID: "no-reads", Number: ptr(150), FirstName: "Анна", LastName: "Иванова", Gender: sptr("female"), CategoryID: sptr("cat")},
		{ID: "start-only", StartTimeMs: ptr(1000)},
		{ID: "split-only"},
		{ID: "stale-clean", CleanTime: sptr("00:10:00.000")},
	} {
		m.EventID, m.RaceID = "ev", "race"
		if err := store.UpsertMember(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.UpsertCheckpoint(ctx, domain.Checkpoint{ID: "split", EventID: "ev", RaceID: "race", Type: domain.CheckpointMid, Sort: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO results (event_id, race_id, member_id, checkpoint_id, time_ms) VALUES ('ev', 'race', 'split-only', 'split', 1500)`); err != nil {
		t.Fatal(err)
	}
	counted := &countedProtocolStore{ProtocolStore: store}
	after, err := BuildProtocol(ctx, counted, "race")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Rows, after.Rows) {
		t.Fatalf("ranked rows changed: before=%+v after=%+v", before.Rows, after.Rows)
	}
	if counted.membersCalls != 1 || counted.lastPassCalls != 0 {
		t.Fatalf("appendix reloaded members/passes: %+v", counted)
	}
	if after.Counts.Finished != before.Counts.Finished || after.Counts.Total != before.Counts.Total+4 {
		t.Fatalf("unexpected existing header counts: %+v", after.Counts)
	}
	if len(after.UnfinishedRows) != 4 {
		t.Fatalf("unfinished rows = %+v", after.UnfinishedRows)
	}
	for i, id := range []string{"no-reads", "split-only", "stale-clean", "start-only"} {
		row := after.UnfinishedRows[i]
		if row.MemberID != id || row.Place != nil || row.GenderPlace != nil || row.CategoryPlace != nil || row.CleanTime != nil || row.CleanTimeMs != nil {
			t.Fatalf("unfinished row acquired result data: %+v", row)
		}
	}
	row := after.UnfinishedRows[0]
	if *row.Number != 150 || row.FirstName != "Анна" || *row.Gender != "female" || *row.CategoryID != "cat" || *row.CategoryName != "Ж18-39" {
		t.Fatalf("missing judge filter/detail fields: %+v", row)
	}
}

func TestBuildProtocolUnfinishedRefreshAfterFinishAndJudgeStatus(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	if err := store.UpsertEvent(ctx, domain.Event{ID: "ev"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRace(ctx, domain.Race{ID: "race", EventID: "ev", Format: domain.FormatFixedDistance}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.MemberStatus{domain.StatusOK, domain.StatusDNS, domain.StatusDNF, domain.StatusDSQ} {
		t.Run(map[domain.MemberStatus]string{domain.StatusOK: "finish", domain.StatusDNS: "dns", domain.StatusDNF: "dnf", domain.StatusDSQ: "dsq"}[status], func(t *testing.T) {
			m := domain.Member{ID: "member", EventID: "ev", RaceID: "race", StartTimeMs: ptr(1000)}
			if err := store.UpsertMember(ctx, m); err != nil {
				t.Fatal(err)
			}
			before, err := BuildProtocol(ctx, store, "race")
			if err != nil || len(before.UnfinishedRows) != 1 || len(before.Rows) != 0 {
				t.Fatalf("before refresh: %+v, %v", before, err)
			}
			m.Status = status
			if status == domain.StatusOK {
				m.FinishTimeMs, m.CleanTime = ptr(2000), sptr("00:00:01.000")
			}
			if err := store.UpsertMember(ctx, m); err != nil {
				t.Fatal(err)
			}
			after, err := BuildProtocol(ctx, store, "race")
			if err != nil || len(after.UnfinishedRows) != 0 || len(after.Rows) != 1 {
				t.Fatalf("after refresh: %+v, %v", after, err)
			}
			if (after.Rows[0].Place != nil) != (status == domain.StatusOK) {
				t.Fatalf("status changed rank policy: %+v", after.Rows[0])
			}
		})
	}
}

func TestBuildProtocolTimeLimitedRankedPassIsNotUnfinished(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	if err := store.UpsertEvent(ctx, domain.Event{ID: "ev"}); err != nil {
		t.Fatal(err)
	}
	race := domain.Race{ID: "race", EventID: "ev", Format: domain.FormatTimeLimited, TimeLimitSeconds: ptr(3600)}
	if err := store.UpsertRace(ctx, race); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertCheckpoint(ctx, domain.Checkpoint{ID: "lap", EventID: "ev", RaceID: "race", Name: "Круг", Type: domain.CheckpointMid, Sort: 2}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"with-pass", "without-pass"} {
		if err := store.UpsertMember(ctx, domain.Member{ID: id, EventID: "ev", RaceID: "race", StartTimeMs: ptr(1000)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO results (event_id, race_id, member_id, checkpoint_id, time_ms) VALUES ('ev', 'race', 'with-pass', 'lap', 2000)`); err != nil {
		t.Fatal(err)
	}
	counted := &countedProtocolStore{ProtocolStore: store}
	p, err := BuildProtocol(ctx, counted, "race")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Rows) != 1 || p.Rows[0].MemberID != "with-pass" || p.Rows[0].Place == nil || *p.Rows[0].Place != 1 || *p.Rows[0].LastCheckpointName != "Круг" {
		t.Fatalf("TimeLimited official result lost: %+v", p.Rows)
	}
	if len(p.UnfinishedRows) != 1 || p.UnfinishedRows[0].MemberID != "without-pass" {
		t.Fatalf("TimeLimited ranked member duplicated: %+v", p.UnfinishedRows)
	}
	if counted.membersCalls != 1 || counted.lastPassCalls != 1 {
		t.Fatalf("added per-member reads: %+v", counted)
	}
}

type countedProtocolStore struct {
	ProtocolStore
	membersCalls  int
	lastPassCalls int
}

func (s *countedProtocolStore) ListMembersByRace(ctx context.Context, raceID string) ([]domain.Member, error) {
	s.membersCalls++
	return s.ProtocolStore.ListMembersByRace(ctx, raceID)
}

func (s *countedProtocolStore) LastPassesInWindow(ctx context.Context, race domain.Race) (map[string]ranking.LastPass, error) {
	s.lastPassCalls++
	return s.ProtocolStore.LastPassesInWindow(ctx, race)
}
