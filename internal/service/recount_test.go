package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	timing "gitlab.com/fightmaster1/timing-core"
)

func ptr(v int64) *int64    { return &v }
func sptr(v string) *string { return &v }

// Full integration: START and FINISH checkpoints on separate reader boards, a
// sleep window on FINISH, a disabled log, and a member without a start read
// (backfilled from race start). Mirrors a real course setup.
func TestRecountDerivesResultsAndMemberTimes(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "event.chrono"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	store, err := sqlite.New(db)
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	const (
		startBoard  = "Feibot:U100"            // start-line reader
		finishBoard = "Feibot:U659"            // finish-line reader
		t0          = int64(1_750_000_000_000) // race start, unix ms
	)

	if err := store.UpsertEvent(ctx, domain.Event{ID: "ev1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRace(ctx, domain.Race{
		ID: "r1", EventID: "ev1", Name: "10k", Format: domain.FormatFixedDistance, StartedAtMs: ptr(t0),
	}); err != nil {
		t.Fatal(err)
	}
	for _, cp := range []domain.Checkpoint{
		{ID: "cp-start", EventID: "ev1", RaceID: "r1", Type: domain.CheckpointStart, Sort: 1, Board: startBoard},
		{ID: "cp-finish", EventID: "ev1", RaceID: "r1", Type: domain.CheckpointFinish, Sort: 2, Board: finishBoard,
			SleepAfterPrevSeconds: ptr(60)},
	} {
		if err := store.UpsertCheckpoint(ctx, cp); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []domain.Member{
		{ID: "mA", EventID: "ev1", RaceID: "r1", EPC: sptr("E280AAA"), FirstName: "A"},
		{ID: "mB", EventID: "ev1", RaceID: "r1", EPC: sptr("E280BBB"), FirstName: "B"},
	} {
		if err := store.UpsertMember(ctx, m); err != nil {
			t.Fatal(err)
		}
	}

	logs := []domain.RfidLog{
		// A: start read 10s after race start.
		{ID: "l1", EventID: "ev1", TimeMs: t0 + 10_000, EPC: "E280AAA", Board: startBoard, Ant: 1},
		// A: finish reader sees the tag 1s later — blocked by the 60s sleep.
		{ID: "l2", EventID: "ev1", TimeMs: t0 + 11_000, EPC: "E280AAA", Board: finishBoard, Ant: 2},
		// A: a judge-disabled read must never produce a result.
		{ID: "l3", EventID: "ev1", TimeMs: t0 + 200_000, EPC: "E280AAA", Board: finishBoard, Ant: 2,
			DisabledAt: ptr(t0 + 300_000)},
		// A: real finish.
		{ID: "l4", EventID: "ev1", TimeMs: t0 + 400_000, EPC: "E280AAA", Board: finishBoard, Ant: 2},
		// B: no start read at all — start must backfill from race started_at.
		{ID: "l5", EventID: "ev1", TimeMs: t0 + 500_000, EPC: "E280BBB", Board: finishBoard, Ant: 2},
		// Unknown tag: ignored.
		{ID: "l6", EventID: "ev1", TimeMs: t0 + 510_000, EPC: "DEADBEEF", Board: finishBoard, Ant: 2},
	}
	if err := store.UpsertRfidLogs(ctx, logs); err != nil {
		t.Fatal(err)
	}

	rec := NewRecounter(store, log.New(io.Discard, "", 0), false)

	for run := 1; run <= 2; run++ { // second run proves idempotency
		stats, err := rec.Recount(ctx, "ev1", "")
		if err != nil {
			t.Fatalf("recount #%d: %v", run, err)
		}
		if stats.LogsReplayed != len(logs) {
			t.Fatalf("run #%d: logs replayed = %d, want %d", run, stats.LogsReplayed, len(logs))
		}

		var resultCount int
		if err := store.DB().QueryRow(`SELECT COUNT(*) FROM results`).Scan(&resultCount); err != nil {
			t.Fatal(err)
		}
		// A: start + finish; B: finish. The sleeping read, the disabled read
		// and the unknown tag produce nothing.
		if resultCount != 3 {
			t.Fatalf("run #%d: results = %d, want 3", run, resultCount)
		}

		assertMemberTimes(t, store, "mA", run, t0+10_000, t0+400_000, "00:06:30")
		assertMemberTimes(t, store, "mB", run, t0, t0+500_000, "00:08:20")
	}
}

func TestRecountPlanTargetsOneMemberAndFailsClosedOnStaleEvidence(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "event.chrono"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := sqlite.New(db)
	if err != nil {
		t.Fatal(err)
	}
	const t0 = int64(1_750_000_000_000)
	if err := store.UpsertEvent(ctx, domain.Event{ID: "ev1", Name: "Targeted"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRace(ctx, domain.Race{ID: "r1", EventID: "ev1", Name: "Race", Format: domain.FormatFixedDistance, StartedAtMs: ptr(t0)}); err != nil {
		t.Fatal(err)
	}
	for _, checkpoint := range []domain.Checkpoint{
		{ID: "start", EventID: "ev1", RaceID: "r1", Type: domain.CheckpointStart, Sort: 1, Board: "start"},
		{ID: "finish", EventID: "ev1", RaceID: "r1", Type: domain.CheckpointFinish, Sort: 2, Board: "finish"},
	} {
		if err := store.UpsertCheckpoint(ctx, checkpoint); err != nil {
			t.Fatal(err)
		}
	}
	for _, member := range []domain.Member{
		{ID: "mA", EventID: "ev1", RaceID: "r1", EPC: sptr("A")},
		{ID: "mB", EventID: "ev1", RaceID: "r1", EPC: sptr("B")},
	} {
		if err := store.UpsertMember(ctx, member); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.UpsertRfidLogs(ctx, []domain.RfidLog{
		{ID: "a-start", EventID: "ev1", EPC: "A", Board: "start", TimeMs: t0 + 10_000},
		{ID: "a-finish", EventID: "ev1", EPC: "A", Board: "finish", TimeMs: t0 + 400_000},
		{ID: "b-start", EventID: "ev1", EPC: "B", Board: "start", TimeMs: t0 + 20_000},
		{ID: "b-finish", EventID: "ev1", EPC: "B", Board: "finish", TimeMs: t0 + 500_000},
	}); err != nil {
		t.Fatal(err)
	}
	recounter := NewRecounter(store, log.New(io.Discard, "", 0), false)
	if _, err := recounter.Recount(ctx, "ev1", ""); err != nil {
		t.Fatal(err)
	}
	var bResultID, bFinish int64
	if err := store.DB().QueryRow(`SELECT id FROM results WHERE member_id='mB' AND checkpoint_id='finish'`).Scan(&bResultID); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow(`SELECT finish_time_ms FROM members WHERE id='mB'`).Scan(&bFinish); err != nil {
		t.Fatal(err)
	}

	if err := store.UpsertRfidLogs(ctx, []domain.RfidLog{{
		ID: "a-earlier-finish", EventID: "ev1", EPC: "A", Board: "finish", TimeMs: t0 + 300_000,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSyncConfig(ctx, "ev1", "https://example.invalid", "token"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`UPDATE sync_config SET projection_pending=1 WHERE event_id='ev1'`); err != nil {
		t.Fatal(err)
	}
	evidence, err := store.ProjectionFenceEvidence(ctx, "ev1")
	if err != nil {
		t.Fatal(err)
	}
	memberID, raceID := "mA", "r1"
	plan := timing.BuildProjectionPlan([]timing.ProjectionChange[string, string]{{
		MemberID: &memberID, RaceID: &raceID, Scope: timing.ImpactReplayMember,
		ConfigVersion: evidence.Exact.ConfigVersion, InputWatermark: evidence.Exact.InputWatermark,
	}})
	stats, executed, err := recounter.RecountPlan(ctx, "ev1", plan, evidence)
	if err != nil || !executed {
		t.Fatalf("targeted recount executed=%v stats=%+v err=%v", executed, stats, err)
	}
	if stats.MembersReplayed != 1 || stats.EventReplayed || stats.LogsReplayed != 3 || !stats.RevisionEvidenceChecked || stats.RevisionEvidenceMismatch {
		t.Fatalf("targeted stats=%+v", stats)
	}
	if pending, err := store.ProjectionPending(ctx, "ev1"); err != nil || pending {
		t.Fatalf("projection pending=%v err=%v after atomic recount", pending, err)
	}
	var gotBResultID, gotBFinish int64
	if err := store.DB().QueryRow(`SELECT id FROM results WHERE member_id='mB' AND checkpoint_id='finish'`).Scan(&gotBResultID); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow(`SELECT finish_time_ms FROM members WHERE id='mB'`).Scan(&gotBFinish); err != nil {
		t.Fatal(err)
	}
	if gotBResultID != bResultID || gotBFinish != bFinish {
		t.Fatalf("unaffected member changed: result %d->%d finish %d->%d", bResultID, gotBResultID, bFinish, gotBFinish)
	}
	assertMemberTimes(t, store, "mA", 1, t0+10_000, t0+300_000, "00:04:50")

	staleEvidence, err := store.ProjectionFenceEvidence(ctx, "ev1")
	if err != nil {
		t.Fatal(err)
	}
	stalePlan := timing.BuildProjectionPlan([]timing.ProjectionChange[string, string]{{
		MemberID: &memberID, RaceID: &raceID, Scope: timing.ImpactReplayMember,
		ConfigVersion: staleEvidence.Exact.ConfigVersion, InputWatermark: staleEvidence.Exact.InputWatermark,
	}})
	if err := store.UpsertRfidLogs(ctx, []domain.RfidLog{{ID: "late-unknown", EventID: "ev1", EPC: "unknown", Board: "finish", TimeMs: t0 + 600_000}}); err != nil {
		t.Fatal(err)
	}
	stats, executed, err = recounter.RecountPlan(ctx, "ev1", stalePlan, staleEvidence)
	if err != nil || !executed || !stats.EventReplayed || !stats.EvidenceFallback {
		t.Fatalf("stale fallback executed=%v stats=%+v err=%v", executed, stats, err)
	}
	if !stats.RevisionEvidenceChecked || stats.RevisionEvidenceMismatch {
		t.Fatalf("both stale evidence forms should agree: stats=%+v", stats)
	}

	falsePositiveEvidence, err := store.ProjectionFenceEvidence(ctx, "ev1")
	if err != nil {
		t.Fatal(err)
	}
	falsePositivePlan := timing.BuildProjectionPlan([]timing.ProjectionChange[string, string]{{
		MemberID: &memberID, RaceID: &raceID, Scope: timing.ImpactReplayMember,
		ConfigVersion: falsePositiveEvidence.Exact.ConfigVersion, InputWatermark: falsePositiveEvidence.Exact.InputWatermark,
	}})
	if _, err := store.DB().Exec(`UPDATE projection_revisions SET input_revision=input_revision+1 WHERE event_id='ev1'`); err != nil {
		t.Fatal(err)
	}
	stats, executed, err = recounter.RecountPlan(ctx, "ev1", falsePositivePlan, falsePositiveEvidence)
	if err != nil || !executed || !stats.EventReplayed || !stats.EvidenceFallback || !stats.RevisionEvidenceMismatch {
		t.Fatalf("revision-only divergence executed=%v stats=%+v err=%v", executed, stats, err)
	}

	versionEvidence, err := store.ProjectionFenceEvidence(ctx, "ev1")
	if err != nil {
		t.Fatal(err)
	}
	versionPlan := timing.BuildProjectionPlan([]timing.ProjectionChange[string, string]{{
		MemberID: &memberID, RaceID: &raceID, Scope: timing.ImpactReplayMember,
		ConfigVersion: versionEvidence.Exact.ConfigVersion, InputWatermark: versionEvidence.Exact.InputWatermark,
	}})
	versionEvidence.RevisionVersion = "projection-revision-v0"
	stats, executed, err = recounter.RecountPlan(ctx, "ev1", versionPlan, versionEvidence)
	if err != nil || !executed || !stats.EventReplayed || !stats.EvidenceFallback || !stats.RevisionEvidenceMismatch {
		t.Fatalf("revision-version divergence executed=%v stats=%+v err=%v", executed, stats, err)
	}

	uncoveredWriterEvidence, err := store.ProjectionFenceEvidence(ctx, "ev1")
	if err != nil {
		t.Fatal(err)
	}
	uncoveredWriterPlan := timing.BuildProjectionPlan([]timing.ProjectionChange[string, string]{{
		MemberID: &memberID, RaceID: &raceID, Scope: timing.ImpactReplayMember,
		ConfigVersion: uncoveredWriterEvidence.Exact.ConfigVersion, InputWatermark: uncoveredWriterEvidence.Exact.InputWatermark,
	}})
	if _, err := store.DB().Exec(`DROP TRIGGER projection_revision_v1_members_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`UPDATE members SET dob='2000-01-01' WHERE id='mA'`); err != nil {
		t.Fatal(err)
	}
	stats, executed, err = recounter.RecountPlan(ctx, "ev1", uncoveredWriterPlan, uncoveredWriterEvidence)
	if err != nil || !executed || !stats.EventReplayed || !stats.EvidenceFallback || !stats.RevisionEvidenceMismatch {
		t.Fatalf("hash-only divergence executed=%v stats=%+v err=%v", executed, stats, err)
	}

	largeEvidence, err := store.ProjectionFenceEvidence(ctx, "ev1")
	if err != nil {
		t.Fatal(err)
	}
	largeChanges := make([]timing.ProjectionChange[string, string], maxTargetedMembersPerPlan+1)
	for index := range largeChanges {
		id := fmt.Sprintf("member-%04d", index)
		largeChanges[index] = timing.ProjectionChange[string, string]{
			MemberID: &id, RaceID: &raceID, Scope: timing.ImpactReplayMember,
			ConfigVersion: largeEvidence.Exact.ConfigVersion, InputWatermark: largeEvidence.Exact.InputWatermark,
		}
	}
	stats, executed, err = recounter.RecountPlan(ctx, "ev1", timing.BuildProjectionPlan(largeChanges), largeEvidence)
	if err != nil || !executed || !stats.EventReplayed || stats.EvidenceFallback || !stats.RevisionEvidenceChecked || stats.RevisionEvidenceMismatch {
		t.Fatalf("large-plan fallback executed=%v stats=%+v err=%v", executed, stats, err)
	}
	identity := CurrentProjectionEvidenceIdentity()
	parity, err := store.GetProjectionEvidenceParity(ctx, "ev1", identity)
	if err != nil {
		t.Fatal(err)
	}
	if parity.Checks != 6 || parity.Matches != 3 || parity.Mismatches != 3 || parity.HashOnlyMismatches != 1 || parity.RevisionOnlyMismatches != 1 || parity.VersionMismatches != 1 {
		t.Fatalf("durable recount parity=%+v", parity)
	}

	rollbackEvidence, err := store.ProjectionFenceEvidence(ctx, "ev1")
	if err != nil {
		t.Fatal(err)
	}
	rollbackPlan := timing.BuildProjectionPlan([]timing.ProjectionChange[string, string]{{
		MemberID: &memberID, RaceID: &raceID, Scope: timing.ImpactReplayMember,
		ConfigVersion: rollbackEvidence.Exact.ConfigVersion, InputWatermark: rollbackEvidence.Exact.InputWatermark,
	}})
	if _, err := store.DB().Exec(`CREATE TRIGGER reject_recount_result BEFORE INSERT ON results BEGIN SELECT RAISE(ABORT, 'reject recount'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := recounter.RecountPlan(ctx, "ev1", rollbackPlan, rollbackEvidence); err == nil {
		t.Fatal("expected recount failure")
	}
	afterFailedRecount, err := store.GetProjectionEvidenceParity(ctx, "ev1", identity)
	if err != nil {
		t.Fatal(err)
	}
	if afterFailedRecount.Checks != parity.Checks || afterFailedRecount.Matches != parity.Matches || afterFailedRecount.Mismatches != parity.Mismatches {
		t.Fatalf("failed recount committed parity telemetry: before=%+v after=%+v", parity, afterFailedRecount)
	}
	if afterFailedRecount.Attempts != parity.Attempts+1 || afterFailedRecount.ReplayFailures != 1 || afterFailedRecount.LastFailureAtMs == nil || afterFailedRecount.LastFailureClass != "projection_transaction_failed" || afterFailedRecount.AuthoritySwitchBlocked != true {
		t.Fatalf("failed recount not recorded durably: before=%+v after=%+v", parity, afterFailedRecount)
	}

	mismatchEvidence, err := store.ProjectionFenceEvidence(ctx, "ev1")
	if err != nil {
		t.Fatal(err)
	}
	mismatchPlan := timing.BuildProjectionPlan([]timing.ProjectionChange[string, string]{{
		MemberID: &memberID, RaceID: &raceID, Scope: timing.ImpactReplayMember,
		ConfigVersion: mismatchEvidence.Exact.ConfigVersion, InputWatermark: mismatchEvidence.Exact.InputWatermark,
	}})
	mismatchEvidence.RevisionVersion = "projection-revision-v0"
	if _, _, err := recounter.RecountPlan(ctx, "ev1", mismatchPlan, mismatchEvidence); err == nil {
		t.Fatal("expected mismatch fallback recount failure")
	}
	afterMismatchFailure, err := store.GetProjectionEvidenceParity(ctx, "ev1", identity)
	if err != nil {
		t.Fatal(err)
	}
	if afterMismatchFailure.Attempts != afterFailedRecount.Attempts+1 || afterMismatchFailure.Checks != parity.Checks || afterMismatchFailure.ReplayFailures != 2 || !afterMismatchFailure.LastFailureVersionMismatch {
		t.Fatalf("failed mismatch recount not recorded durably: %+v", afterMismatchFailure)
	}
}

// A Stopwatch Checkpoint capture is an ordinary number-only RFID log from a
// different board. Replaying the event must merge it with Chrono Desk's finish
// read instead of treating the desk as the only timing point. This is also a
// cross-system parity guard: rfid-sync and run5's bulk replay must resolve a
// member by number before falling back to EPC, exactly as chrono-desk does.
func TestRecountMergesStopwatchCheckpointWithChronoDeskFinish(t *testing.T) {
	fixture := loadMultiPointFixture(t)
	ctx := context.Background()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "event.chrono"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	store, err := sqlite.New(db)
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	t0 := fixture.Race.StartedAtMs
	if err := store.UpsertEvent(ctx, domain.Event{ID: "ev1", Name: "Multi-point"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRace(ctx, domain.Race{
		ID: "r1", EventID: "ev1", Name: "10k", Format: domain.FormatFixedDistance, StartedAtMs: ptr(t0),
	}); err != nil {
		t.Fatal(err)
	}
	for _, raw := range fixture.Checkpoints {
		cp := domain.Checkpoint{
			ID: "cp-" + raw.Key, EventID: "ev1", RaceID: "r1", Name: raw.Key,
			Type: raw.Type, Sort: raw.Sort, Board: raw.Board,
		}
		if err := store.UpsertCheckpoint(ctx, cp); err != nil {
			t.Fatal(err)
		}
	}
	number := fixture.Member.Number
	if err := store.UpsertMember(ctx, domain.Member{
		ID: "m1", EventID: "ev1", RaceID: "r1", Number: &number, EPC: sptr(fixture.Member.EPC), FirstName: "Runner",
	}); err != nil {
		t.Fatal(err)
	}
	logs := make([]domain.RfidLog, 0, len(fixture.Logs))
	for _, raw := range fixture.Logs {
		logs = append(logs, domain.RfidLog{
			ID: raw.ID, EventID: "ev1", Number: raw.Number, TimeMs: t0 + raw.TimeOffsetMs,
			EPC: raw.EPC, Board: raw.Board,
		})
	}
	if err := store.UpsertRfidLogs(ctx, logs); err != nil {
		t.Fatal(err)
	}

	rec := NewRecounter(store, log.New(io.Discard, "", 0), false)
	for run := 1; run <= 2; run++ {
		if _, err := rec.Recount(ctx, "ev1", ""); err != nil {
			t.Fatalf("recount #%d: %v", run, err)
		}
		for _, expected := range fixture.ExpectedResults {
			logID := expected.RfidLogID
			checkpointID := "cp-" + expected.CheckpointKey
			var got string
			if err := store.DB().QueryRow(
				`SELECT checkpoint_id FROM results WHERE rfid_log_id = ?`, logID,
			).Scan(&got); err != nil {
				t.Fatalf("run #%d result for %s: %v", run, logID, err)
			}
			if got != checkpointID {
				t.Errorf("run #%d log %s checkpoint = %s, want %s", run, logID, got, checkpointID)
			}
		}
		assertMemberTimes(
			t, store, "m1", run,
			t0+fixture.ExpectedMemberTimes.StartOffsetMs,
			t0+fixture.ExpectedMemberTimes.FinishOffsetMs,
			fixture.ExpectedMemberTimes.CleanTime,
		)
	}
}

type multiPointFixture struct {
	FixtureVersion int `json:"fixture_version"`
	Race           struct {
		StartedAtMs int64 `json:"started_at_ms"`
	} `json:"race"`
	Member struct {
		Number int64  `json:"number"`
		EPC    string `json:"epc"`
	} `json:"member"`
	Checkpoints []struct {
		Key   string                `json:"key"`
		Board string                `json:"board"`
		Type  domain.CheckpointType `json:"type"`
		Sort  int64                 `json:"sort"`
	} `json:"checkpoints"`
	Logs []struct {
		ID           string `json:"id"`
		Number       int64  `json:"number"`
		EPC          string `json:"epc"`
		TimeOffsetMs int64  `json:"time_offset_ms"`
		Board        string `json:"board"`
	} `json:"logs"`
	ExpectedResults []struct {
		RfidLogID     string `json:"rfid_log_id"`
		CheckpointKey string `json:"checkpoint_key"`
	} `json:"expected_results"`
	ExpectedMemberTimes struct {
		StartOffsetMs  int64  `json:"start_offset_ms"`
		FinishOffsetMs int64  `json:"finish_offset_ms"`
		CleanTime      string `json:"clean_time"`
	} `json:"expected_member_times"`
}

func loadMultiPointFixture(t *testing.T) multiPointFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "parity", "multi-point-replay-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture multiPointFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.FixtureVersion != 1 {
		t.Fatalf("fixture version = %d, want 1", fixture.FixtureVersion)
	}
	return fixture
}

func assertMemberTimes(t *testing.T, store *sqlite.Store, memberID string, run int, wantStart, wantFinish int64, wantClean string) {
	t.Helper()
	var start, finish *int64
	var clean *string
	err := store.DB().QueryRow(
		`SELECT start_time_ms, finish_time_ms, clean_time FROM members WHERE id = ?`, memberID).
		Scan(&start, &finish, &clean)
	if err != nil {
		t.Fatalf("member %s: %v", memberID, err)
	}
	if start == nil || *start != wantStart {
		t.Errorf("run #%d %s: start = %v, want %d", run, memberID, start, wantStart)
	}
	if finish == nil || *finish != wantFinish {
		t.Errorf("run #%d %s: finish = %v, want %d", run, memberID, finish, wantFinish)
	}
	if clean == nil || *clean != wantClean {
		t.Errorf("run #%d %s: clean = %v, want %q", run, memberID, clean, wantClean)
	}
}
