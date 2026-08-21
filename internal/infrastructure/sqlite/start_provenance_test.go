package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/processor"
)

func TestMemberStartProvenanceColumnsExistAndDefaultToUnknown(t *testing.T) {
	store := newTestStore(t)
	mustStartFixture(t, store)

	if _, err := store.DB().Exec(`
		INSERT INTO members (id, event_id, race_id, number, start_time_ms)
		VALUES ('member-default', 'event-start', 'race-start', 1, 1000)`); err != nil {
		t.Fatal(err)
	}
	var source string
	var observationID sql.NullString
	if err := store.DB().QueryRow(`
		SELECT start_time_source, start_observation_id FROM members WHERE id = 'member-default'`).
		Scan(&source, &observationID); err != nil {
		t.Fatal(err)
	}
	if source != "unknown" || observationID.Valid {
		t.Fatalf("default provenance = %q/%v, want unknown/NULL", source, observationID)
	}
}

func TestWipeClearsDerivedStartButPreservesManualAndUnknown(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	mustStartFixture(t, store)
	for _, row := range []struct {
		id, source string
	}{
		{id: "observation", source: "observation"},
		{id: "race-default", source: "race_default"},
		{id: "manual", source: "manual"},
		{id: "legacy", source: "unknown"},
	} {
		if _, err := store.DB().Exec(`
			INSERT INTO members
				(id, event_id, race_id, number, start_time_ms, start_time_source, start_observation_id)
			VALUES (?, 'event-start', 'race-start', 1, 1000, ?, ?)`,
			row.id, row.source, nullableStartObservation(row.source)); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.WipeDerivedResults(ctx, "event-start", ""); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"observation", "race-default"} {
		var start sql.NullInt64
		var source string
		var observationID sql.NullString
		if err := store.DB().QueryRow(`
			SELECT start_time_ms, start_time_source, start_observation_id FROM members WHERE id = ?`, id).
			Scan(&start, &source, &observationID); err != nil {
			t.Fatal(err)
		}
		if start.Valid || source != "unknown" || observationID.Valid {
			t.Errorf("derived member %s provenance = %v/%q/%v, want NULL/unknown/NULL", id, start, source, observationID)
		}
	}
	for _, row := range []struct {
		id, source string
	}{{"manual", "manual"}, {"legacy", "unknown"}} {
		var start int64
		var source string
		if err := store.DB().QueryRow(`SELECT start_time_ms, start_time_source FROM members WHERE id = ?`, row.id).
			Scan(&start, &source); err != nil {
			t.Fatal(err)
		}
		if start != 1000 || source != row.source {
			t.Errorf("preserved member %s provenance = %d/%q", row.id, start, source)
		}
	}
}

func TestStartCheckpointRecordsObservationProvenance(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	mustStartFixture(t, store)
	if _, err := store.DB().Exec(`
		INSERT INTO members (id, event_id, race_id, number)
		VALUES ('member-observed', 'event-start', 'race-start', 42);
		INSERT INTO checkpoints (id, event_id, race_id, name, type, sort, board)
		VALUES ('checkpoint-start', 'event-start', 'race-start', 'Start', 1, 1, 'B1');
		INSERT INTO rfid_logs (id, event_id, number, time_ms, board)
		VALUES ('log-start', 'event-start', 42, 2000, 'B1')`); err != nil {
		t.Fatal(err)
	}

	entry := domain.RfidLog{ID: "log-start", EventID: "event-start", Number: 42, TimeMs: 2000, Board: "B1"}
	if err := processor.New(NewProcessorRepo(store), nil, false).Process(ctx, entry, ""); err != nil {
		t.Fatal(err)
	}
	var start int64
	var source, observationID string
	if err := store.DB().QueryRow(`
		SELECT start_time_ms, start_time_source, start_observation_id
		FROM members WHERE id = 'member-observed'`).Scan(&start, &source, &observationID); err != nil {
		t.Fatal(err)
	}
	if start != 2000 || source != "observation" || observationID != "log-start" {
		t.Fatalf("observed provenance = %d/%q/%q", start, source, observationID)
	}
}

func TestStartCheckpointDoesNotOverwriteManualStart(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	mustStartFixture(t, store)
	if _, err := store.DB().Exec(`
		INSERT INTO members
			(id, event_id, race_id, number, start_time_ms, start_time_source)
		VALUES ('member-manual', 'event-start', 'race-start', 43, 1500, 'manual');
		INSERT INTO checkpoints (id, event_id, race_id, name, type, sort, board)
		VALUES ('checkpoint-start', 'event-start', 'race-start', 'Start', 1, 1, 'B1');
		INSERT INTO rfid_logs (id, event_id, number, time_ms, board)
		VALUES ('late-start-log', 'event-start', 43, 2000, 'B1')`); err != nil {
		t.Fatal(err)
	}

	entry := domain.RfidLog{ID: "late-start-log", EventID: "event-start", Number: 43, TimeMs: 2000, Board: "B1"}
	if err := processor.New(NewProcessorRepo(store), nil, false).Process(ctx, entry, ""); err != nil {
		t.Fatal(err)
	}
	var start int64
	var source string
	var observationID sql.NullString
	if err := store.DB().QueryRow(`
		SELECT start_time_ms, start_time_source, start_observation_id
		FROM members WHERE id = 'member-manual'`).Scan(&start, &source, &observationID); err != nil {
		t.Fatal(err)
	}
	if start != 1500 || source != "manual" || observationID.Valid {
		t.Fatalf("manual start overwritten: %d/%q/%v", start, source, observationID)
	}
}

func mustStartFixture(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	if err := store.UpsertEvent(ctx, domain.Event{ID: "event-start", Name: "Start provenance"}); err != nil {
		t.Fatal(err)
	}
	started := int64(500)
	if err := store.UpsertRace(ctx, domain.Race{
		ID: "race-start", EventID: "event-start", Name: "Race", StartedAtMs: &started,
	}); err != nil {
		t.Fatal(err)
	}
}

func nullableStartObservation(source string) any {
	if source == "observation" {
		return "start-log"
	}
	return nil
}
