package sqlite

import (
	"context"
	"errors"
	"slices"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

func TestProjectionRevisionTriggersCoverEveryEvidenceTable(t *testing.T) {
	store := newChangeFeedStore(t)
	rows, err := store.DB().Query(`SELECT name FROM sqlite_master
		WHERE type='trigger' AND name LIKE 'projection_revision_v1_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		got = append(got, name)
	}
	want := []string{
		"projection_revision_v1_categories_update",
		"projection_revision_v1_checkpoints_delete",
		"projection_revision_v1_checkpoints_insert",
		"projection_revision_v1_checkpoints_update",
		"projection_revision_v1_events_delete",
		"projection_revision_v1_events_insert",
		"projection_revision_v1_events_update",
		"projection_revision_v1_members_delete",
		"projection_revision_v1_members_insert",
		"projection_revision_v1_members_update",
		"projection_revision_v1_race_categories_delete",
		"projection_revision_v1_race_categories_insert",
		"projection_revision_v1_race_categories_update",
		"projection_revision_v1_races_delete",
		"projection_revision_v1_races_insert",
		"projection_revision_v1_races_update",
		"projection_revision_v1_results_delete",
		"projection_revision_v1_results_insert",
		"projection_revision_v1_results_update",
		"projection_revision_v1_rfid_logs_delete",
		"projection_revision_v1_rfid_logs_insert",
		"projection_revision_v1_rfid_logs_update",
		"projection_revision_v1_sync_config_delete",
		"projection_revision_v1_sync_config_insert",
		"projection_revision_v1_sync_config_update",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("revision triggers=\n%v\nwant=\n%v", got, want)
	}
}

func TestProjectionRevisionsTrackConfigAndInputInTheirTransactions(t *testing.T) {
	store := newChangeFeedStore(t)
	ctx := context.Background()
	if err := store.UpsertRace(ctx, domain.Race{ID: "r1", EventID: "ev1", Name: "Race", Format: domain.FormatFixedDistance}); err != nil {
		t.Fatal(err)
	}
	number := int64(7)
	if err := store.UpsertMember(ctx, domain.Member{ID: "m1", EventID: "ev1", RaceID: "r1", Number: &number}); err != nil {
		t.Fatal(err)
	}

	initial, err := store.ProjectionRevisions(ctx, "ev1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertMember(ctx, domain.Member{ID: "m1", EventID: "ev1", RaceID: "r1", Number: &number}); err != nil {
		t.Fatal(err)
	}
	afterNoop, err := store.ProjectionRevisions(ctx, "ev1")
	if err != nil || afterNoop != initial {
		t.Fatalf("no-op upsert changed revisions: initial=%+v after=%+v err=%v", initial, afterNoop, err)
	}
	if _, err := store.DB().Exec(`UPDATE members SET dob='2000-01-01' WHERE id='m1'`); err != nil {
		t.Fatal(err)
	}
	configured, err := store.ProjectionRevisions(ctx, "ev1")
	if err != nil {
		t.Fatal(err)
	}
	if configured.ConfigRevision <= initial.ConfigRevision || configured.InputRevision != initial.InputRevision {
		t.Fatalf("config revision initial=%+v configured=%+v", initial, configured)
	}

	minimum, maximum, male := 18, 39, "male"
	if err := store.UpsertCategory(ctx, domain.Category{ID: "cat1", Name: "Adults", Min: &minimum, Max: &maximum, Gender: &male}); err != nil {
		t.Fatal(err)
	}
	unattached, err := store.ProjectionRevisions(ctx, "ev1")
	if err != nil || unattached != configured {
		t.Fatalf("unattached category changed revisions: configured=%+v unattached=%+v err=%v", configured, unattached, err)
	}
	if err := store.AttachRaceCategory(ctx, "r1", "cat1"); err != nil {
		t.Fatal(err)
	}
	attached, err := store.ProjectionRevisions(ctx, "ev1")
	if err != nil || attached.ConfigRevision <= unattached.ConfigRevision {
		t.Fatalf("category attach not tracked: unattached=%+v attached=%+v err=%v", unattached, attached, err)
	}
	if err := store.UpsertCategory(ctx, domain.Category{ID: "cat1", Name: "Adults 18–39", Min: &minimum, Max: &maximum, Gender: &male}); err != nil {
		t.Fatal(err)
	}
	categoryChanged, err := store.ProjectionRevisions(ctx, "ev1")
	if err != nil || categoryChanged.ConfigRevision <= attached.ConfigRevision {
		t.Fatalf("attached category update not tracked: attached=%+v changed=%+v err=%v", attached, categoryChanged, err)
	}
	configured = categoryChanged

	if err := store.ApplyObservationFeedPage(ctx, "ev1", []domain.RfidLog{{
		ID: "log-1", EventID: "ev1", Number: 7, TimeMs: 1000, Board: "finish",
	}}, "cursor-1", 1000); err != nil {
		t.Fatal(err)
	}
	withInput, err := store.ProjectionRevisions(ctx, "ev1")
	if err != nil {
		t.Fatal(err)
	}
	if withInput.InputRevision <= configured.InputRevision || withInput.ConfigRevision != configured.ConfigRevision {
		t.Fatalf("input revision configured=%+v withInput=%+v", configured, withInput)
	}

	wantRollback := withInput
	err = store.WithinTx(ctx, func(txStore *Store) error {
		if _, err := txStore.db.ExecContext(ctx, `UPDATE rfid_logs SET disabled_at=123 WHERE id='log-1'`); err != nil {
			return err
		}
		if _, err := txStore.db.ExecContext(ctx, `UPDATE members SET gender='female' WHERE id='m1'`); err != nil {
			return err
		}
		return errors.New("force rollback")
	})
	if err == nil {
		t.Fatal("expected rollback error")
	}
	afterRollback, err := store.ProjectionRevisions(ctx, "ev1")
	if err != nil || afterRollback != wantRollback {
		t.Fatalf("revisions after rollback=%+v err=%v, want %+v", afterRollback, err, wantRollback)
	}
}

func TestProjectionRevisionsIgnoreDerivedResultsButTrackManualResults(t *testing.T) {
	store := newChangeFeedStore(t)
	ctx := context.Background()
	if err := store.UpsertRace(ctx, domain.Race{ID: "r1", EventID: "ev1", Name: "Race", Format: domain.FormatFixedDistance}); err != nil {
		t.Fatal(err)
	}
	number := int64(7)
	if err := store.UpsertMember(ctx, domain.Member{ID: "m1", EventID: "ev1", RaceID: "r1", Number: &number}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertCheckpoint(ctx, domain.Checkpoint{ID: "cp1", EventID: "ev1", RaceID: "r1", Type: domain.CheckpointFinish, Board: "finish"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertRfidLogs(ctx, []domain.RfidLog{{ID: "log-1", EventID: "ev1", Number: 7, TimeMs: 1000, Board: "finish"}}); err != nil {
		t.Fatal(err)
	}

	baseline, err := store.ProjectionRevisions(ctx, "ev1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`INSERT INTO results
		(event_id, race_id, member_id, checkpoint_id, rfid_log_id, time_ms, number)
		VALUES ('ev1', 'r1', 'm1', 'cp1', 'log-1', 1000, 7)`); err != nil {
		t.Fatal(err)
	}
	derived, err := store.ProjectionRevisions(ctx, "ev1")
	if err != nil || derived.InputRevision != baseline.InputRevision {
		t.Fatalf("derived result changed input revision: baseline=%+v derived=%+v err=%v", baseline, derived, err)
	}
	if _, err := store.DB().Exec(`INSERT INTO results
		(event_id, race_id, member_id, checkpoint_id, rfid_log_id, time_ms, number)
		VALUES ('ev1', 'r1', 'm1', NULL, NULL, 1100, 7)`); err != nil {
		t.Fatal(err)
	}
	manual, err := store.ProjectionRevisions(ctx, "ev1")
	if err != nil || manual.InputRevision <= derived.InputRevision {
		t.Fatalf("manual result did not change input revision: derived=%+v manual=%+v err=%v", derived, manual, err)
	}
}

func TestProjectionFenceEvidenceReadsExactAndRevisionsFromOneSnapshot(t *testing.T) {
	store := newChangeFeedStore(t)
	ctx := context.Background()
	if err := store.UpsertRace(ctx, domain.Race{ID: "r1", EventID: "ev1", Name: "Race", Format: domain.FormatFixedDistance}); err != nil {
		t.Fatal(err)
	}

	fence, err := store.ProjectionFenceEvidence(ctx, "ev1")
	if err != nil {
		t.Fatal(err)
	}
	if fence.RevisionVersion != ProjectionRevisionVersion {
		t.Fatalf("revision version=%q, want %q", fence.RevisionVersion, ProjectionRevisionVersion)
	}
	if fence.Exact.ConfigVersion == "" || fence.Exact.InputWatermark == "" || fence.Revisions.ConfigRevision == 0 {
		t.Fatalf("incomplete fence evidence: %+v", fence)
	}
}

func TestProjectionEvidenceParityAccumulatesFieldAcceptanceCounters(t *testing.T) {
	store := newChangeFeedStore(t)
	ctx := context.Background()
	checks := []ProjectionEvidenceCheck{
		{CheckedAtMs: 1000},
		{ExactChanged: true, RevisionChanged: true, CheckedAtMs: 2000},
		{ExactChanged: true, CheckedAtMs: 3000},
		{RevisionChanged: true, CheckedAtMs: 4000},
		{VersionMismatch: true, CheckedAtMs: 5000},
	}
	for _, check := range checks {
		if err := store.RecordProjectionEvidenceCheck(ctx, "ev1", check); err != nil {
			t.Fatal(err)
		}
	}

	parity, err := store.GetProjectionEvidenceParity(ctx, "ev1")
	if err != nil {
		t.Fatal(err)
	}
	if parity.RevisionVersion != ProjectionRevisionVersion || parity.Checks != 5 || parity.Matches != 2 || parity.Mismatches != 3 || parity.HashOnlyMismatches != 1 || parity.RevisionOnlyMismatches != 1 || parity.VersionMismatches != 1 || parity.LastCheckedAtMs != 5000 || parity.LastMismatchAtMs == nil || *parity.LastMismatchAtMs != 5000 {
		t.Fatalf("parity=%+v", parity)
	}
}

func TestProjectionEvidenceTracksResultConfigurationAndInputs(t *testing.T) {
	store := newChangeFeedStore(t)
	ctx := context.Background()
	if err := store.UpsertRace(ctx, domain.Race{ID: "r1", EventID: "ev1", Name: "Race", Format: domain.FormatFixedDistance}); err != nil {
		t.Fatal(err)
	}
	number := int64(7)
	if err := store.UpsertMember(ctx, domain.Member{ID: "m1", EventID: "ev1", RaceID: "r1", Number: &number}); err != nil {
		t.Fatal(err)
	}

	initial, err := store.ProjectionEvidence(ctx, "ev1")
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := store.ProjectionEvidence(ctx, "ev1")
	if err != nil || repeated != initial {
		t.Fatalf("unstable evidence: initial=%+v repeated=%+v err=%v", initial, repeated, err)
	}

	if _, err := store.DB().Exec(`INSERT INTO categories (id, name, min, max, gender) VALUES ('cat1', 'Masters', 40, 49, 'male')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`UPDATE members SET category_id='cat1', dob='1980-01-01', gender='male' WHERE id='m1'`); err != nil {
		t.Fatal(err)
	}
	configured, err := store.ProjectionEvidence(ctx, "ev1")
	if err != nil {
		t.Fatal(err)
	}
	if configured.ConfigVersion == initial.ConfigVersion || configured.InputWatermark != initial.InputWatermark {
		t.Fatalf("configuration evidence drift: initial=%+v configured=%+v", initial, configured)
	}

	if err := store.ApplyObservationFeedPage(ctx, "ev1", []domain.RfidLog{{
		ID: "log-1", EventID: "ev1", Number: 7, TimeMs: 1000, Board: "split",
	}}, "cursor-1", 1000); err != nil {
		t.Fatal(err)
	}
	withInput, err := store.ProjectionEvidence(ctx, "ev1")
	if err != nil {
		t.Fatal(err)
	}
	if withInput.ConfigVersion != configured.ConfigVersion || withInput.InputWatermark == configured.InputWatermark {
		t.Fatalf("input evidence drift: configured=%+v input=%+v", configured, withInput)
	}
}

func TestResolveObservationMemberRejectsAmbiguousNumber(t *testing.T) {
	store := newChangeFeedStore(t)
	ctx := context.Background()
	for _, raceID := range []string{"r1", "r2"} {
		if err := store.UpsertRace(ctx, domain.Race{ID: raceID, EventID: "ev1", Name: raceID, Format: domain.FormatFixedDistance}); err != nil {
			t.Fatal(err)
		}
		number := int64(42)
		if err := store.UpsertMember(ctx, domain.Member{ID: "m-" + raceID, EventID: "ev1", RaceID: raceID, Number: &number}); err != nil {
			t.Fatal(err)
		}
	}
	match, err := store.ResolveObservationMember(ctx, "ev1", 42, "")
	if err != nil {
		t.Fatal(err)
	}
	if !match.Ambiguous || match.Found {
		t.Fatalf("match=%+v, want ambiguous", match)
	}
}
