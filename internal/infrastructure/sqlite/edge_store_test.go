package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/rfid-core/edge"
	"gitlab.com/fightmaster1/rfid-core/ingest"
)

func edgeStoreFixture(t *testing.T) (*Store, ingest.Event) {
	t.Helper()
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.UpsertEvent(ctx, domain.Event{ID: "100", Name: "Synthetic edge event"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertRace(ctx, domain.Race{ID: "r1", EventID: "100", Name: "Race"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertCheckpoint(ctx, domain.Checkpoint{ID: "cp1", EventID: "100", RaceID: "r1", Board: "plate-test", Type: domain.CheckpointType(3)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEdgeBindings(ctx, "100", []domain.EdgeBinding{{Board: "plate-test", SourceSessionID: "source-session"}}); err != nil {
		t.Fatal(err)
	}
	instant := time.Date(2026, 9, 10, 8, 0, 0, 123000000, time.UTC)
	ev := ingest.Event{EdgeVersion: 1, ExternalEventID: 100, SourceSessionID: "source-session", IdentityProfile: edge.IdentityRFID, ClockEvidenceID: "clock-test", ClockQuality: edge.ClockOperator,
		Board: "plate-test", EPC: "E280AABB", Time: instant.UnixMilli(), RTC: instant.Format(time.RFC3339Nano), Ant: 1, Number: 12, RSSI: -50,
		ObservationVersion: 1, CaptureSourceID: "plate:source-session", OriginSystem: "edge", OriginInstanceID: "plate-installation", OriginSequence: 7}
	ev.ID = ingest.RFIDReadID(ev.Board, ev.EPC, ev.Time, ev.Ant)
	return s, ev
}

func acceptEdge(s *Store, eventID string, ev ingest.Event) (EdgeAcceptance, error) {
	var result EdgeAcceptance
	err := s.WithinTx(context.Background(), func(tx *Store) error {
		var err error
		result, err = tx.AcceptEdgeObservation(context.Background(), eventID, ev)
		return err
	})
	return result, err
}

func TestEdgeStoragePreservesProducerWireAndIndependentRelayJournal(t *testing.T) {
	s, ev := edgeStoreFixture(t)
	ctx := context.Background()
	if _, err := s.AcceptEdgeObservation(ctx, "100", ev); err == nil {
		t.Fatal("accepted without transaction")
	}
	result, err := acceptEdge(s, "100", ev)
	if err != nil || !result.Inserted || result.Log.OriginSystem != "edge" || result.Log.OriginInstanceID != "plate-installation" || result.Log.OriginSequence != 7 {
		t.Fatalf("%+v %v", result, err)
	}
	items, err := s.EdgeJournal(ctx, "100", 0, 10)
	if err != nil || len(items) != 1 || items[0].State != "pending" {
		t.Fatalf("%+v %v", items, err)
	}
	expected, _ := edge.Encode(ev)
	if string(items[0].Payload) != string(expected) {
		t.Fatal("source session/clock/provenance changed")
	}
	if _, err := s.DB().Exec(`UPDATE rfid_logs SET disabled_at=1234 WHERE id=?`, ev.ID); err != nil {
		t.Fatal(err)
	}
	ev.OriginInstanceID = "another-hop"
	ev.ClockEvidenceID = "another-clock"
	if result, err = acceptEdge(s, "100", ev); err != nil || result.Inserted || result.Log.DisabledAt == nil || result.Log.OriginInstanceID != "plate-installation" {
		t.Fatalf("duplicate rewrote original: %+v %v", result, err)
	}
	items, _ = s.EdgeJournal(ctx, "100", 0, 10)
	if len(items) != 1 || string(items[0].Payload) != string(expected) {
		t.Fatal("duplicate changed relay payload")
	}
	var nativeCount int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM observation_outbox`).Scan(&nativeCount); err != nil || nativeCount != 0 {
		t.Fatal("edge observation became desk-owned v3")
	}
}

func TestEdgeStorageRejectsWrongEventSessionGrantAndContent(t *testing.T) {
	for _, kind := range []string{"event", "session", "board", "grant revoked", "content"} {
		t.Run(kind, func(t *testing.T) {
			s, ev := edgeStoreFixture(t)
			switch kind {
			case "event":
				ev.ExternalEventID = 200
			case "session":
				ev.SourceSessionID = "other-session"
			case "board":
				ev.Board = "unknown"
				ev.ID = ingest.RFIDReadID(ev.Board, ev.EPC, ev.Time, ev.Ant)
			case "grant revoked":
				if err := s.SetEdgeBindings(context.Background(), "100", nil); err != nil {
					t.Fatal(err)
				}
			case "content":
				if _, err := acceptEdge(s, "100", ev); err != nil {
					t.Fatal(err)
				}
				ev.Number++
			}
			if _, err := acceptEdge(s, "100", ev); err == nil {
				t.Fatal("accepted wrong binding/content")
			}
		})
	}
}

func TestEdgeForeignLegacyAliasDoesNotAcquireOwnership(t *testing.T) {
	s, ev := edgeStoreFixture(t)
	ctx := context.Background()
	original := domain.RfidLog{ID: "historical-site-id", EventID: "100", TimeMs: ev.Time, Ant: ev.Ant, EPC: "e280aabb", Number: ev.Number, Board: ev.Board, OriginSystem: "site"}
	if _, err := s.InsertRfidLogs(ctx, []domain.RfidLog{original}); err != nil {
		t.Fatal(err)
	}
	result, err := acceptEdge(s, "100", ev)
	if err != nil || result.Inserted || result.Log.ID != original.ID || result.Log.OriginSystem != "site" {
		t.Fatalf("legacy alias: %+v %v", result, err)
	}
	items, _ := s.EdgeJournal(ctx, "100", 0, 10)
	if len(items) != 0 {
		t.Fatal("imported observation became relay-owned")
	}
	if n, _ := s.CountRfidLogs(ctx, "100"); n != 1 {
		t.Fatalf("physical duplicate count=%d", n)
	}
}

func TestEdgeJournalAndProjectionFailureRollbackRawAcceptance(t *testing.T) {
	for _, kind := range []string{"journal", "projection"} {
		t.Run(kind, func(t *testing.T) {
			s, ev := edgeStoreFixture(t)
			ctx := context.Background()
			if kind == "journal" {
				if _, err := s.DB().Exec(`CREATE TRIGGER fail_edge_journal BEFORE INSERT ON edge_observation_outbox BEGIN SELECT RAISE(ABORT,'disk fault'); END`); err != nil {
					t.Fatal(err)
				}
			}
			err := s.WithinTx(ctx, func(tx *Store) error {
				if _, err := tx.AcceptEdgeObservation(ctx, "100", ev); err != nil {
					return err
				}
				return errors.New("projection failure before commit")
			})
			if err == nil {
				t.Fatal("fault not injected")
			}
			if n, _ := s.CountRfidLogs(ctx, "100"); n != 0 {
				t.Fatal("failed acceptance left raw row")
			}
			if items, _ := s.EdgeJournal(ctx, "100", 0, 10); len(items) != 0 {
				t.Fatal("failed acceptance left outbox")
			}
		})
	}
}

func TestEdgeBindingChangesAreAtomicAndKeepOldJournal(t *testing.T) {
	s, ev := edgeStoreFixture(t)
	ctx := context.Background()
	if _, err := acceptEdge(s, "100", ev); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`CREATE TRIGGER fail_edge_binding BEFORE INSERT ON local_changes WHEN NEW.entity='edge_binding' BEGIN SELECT RAISE(ABORT,'journal unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEdgeBindings(ctx, "100", []domain.EdgeBinding{{Board: ev.Board, SourceSessionID: "next-session"}}); err == nil {
		t.Fatal("unaudited bindings accepted")
	}
	bindings, _ := s.EdgeBindings(ctx, "100")
	if len(bindings) != 1 || bindings[0].SourceSessionID != ev.SourceSessionID {
		t.Fatal("failed settings change was not rolled back")
	}
	items, _ := s.EdgeJournal(ctx, "100", 0, 10)
	if len(items) != 1 {
		t.Fatal("binding change modified journal")
	}
}

func TestEdgeAdditiveTablesAndRestartPreserveRawAndPendingWire(t *testing.T) {
	s, ev := edgeStoreFixture(t)
	ctx := context.Background()
	disabled := int64(1234)
	foreign := domain.RfidLog{ID: "old-site-row", EventID: "100", Board: "old-board", TimeMs: 123, DisabledAt: &disabled, OriginSystem: "site"}
	if _, err := s.InsertRfidLogs(ctx, []domain.RfidLog{foreign}); err != nil {
		t.Fatal(err)
	}
	// Simulate the pre-edge schema, with an existing event and judge state.
	if _, err := s.DB().Exec(`DROP TABLE edge_observation_outbox; DROP TABLE edge_bindings;`); err != nil {
		t.Fatal(err)
	}
	if _, err := New(s.DB()); err != nil {
		t.Fatal(err)
	}
	if bindings, err := s.EdgeBindings(ctx, "100"); err != nil || len(bindings) != 0 {
		t.Fatalf("migration invented bindings: %+v %v", bindings, err)
	}
	if err := s.SetEdgeBindings(ctx, "100", []domain.EdgeBinding{{Board: ev.Board, SourceSessionID: ev.SourceSessionID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := acceptEdge(s, "100", ev); err != nil {
		t.Fatal(err)
	}
	var path string
	if err := s.DB().QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	reopened, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	if accepted, err := acceptEdge(reopened, "100", ev); err != nil || accepted.Inserted {
		t.Fatalf("restart repeat: %+v %v", accepted, err)
	}
	items, err := reopened.EdgeJournal(ctx, "100", 0, 10)
	want, _ := edge.Encode(ev)
	if err != nil || len(items) != 1 || items[0].State != "pending" || string(items[0].Payload) != string(want) {
		t.Fatalf("restart changed pending wire: %+v %v", items, err)
	}
	stored, found, err := reopened.findRfidLog(ctx, foreign.ID)
	if err != nil || !found || stored.DisabledAt == nil || *stored.DisabledAt != disabled || stored.OriginSystem != "site" || stored.TimeMs != foreign.TimeMs {
		t.Fatalf("migration/restart changed old row: %+v %v", stored, err)
	}
}

func TestAutomaticFeibotMigrationPreservesExplicitAndRevokedHistory(t *testing.T) {
	for _, state := range []string{"fresh", "explicit", "revoked"} {
		t.Run(state, func(t *testing.T) {
			s, _ := edgeStoreFixture(t)
			if state == "revoked" {
				if err := s.SetEdgeBindings(t.Context(), "100", nil); err != nil {
					t.Fatal(err)
				}
			}
			if state == "fresh" {
				if _, err := s.DB().Exec(`DELETE FROM edge_bindings; DELETE FROM local_changes WHERE entity='edge_binding'`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.DB().Exec(`DROP TABLE edge_input_policy`); err != nil {
				t.Fatal(err)
			}
			for n := 0; n < 2; n++ {
				migrated, err := New(s.DB())
				if err != nil {
					t.Fatal(err)
				}
				allowed, err := migrated.AutomaticFeibotInput(t.Context(), "100")
				if err != nil || allowed != (state == "fresh") {
					t.Fatalf("migration %s: allowed=%v error=%v", state, allowed, err)
				}
			}
		})
	}
}

func TestFailedExplicitRestrictionDoesNotDisableAutomaticFeibot(t *testing.T) {
	s, _ := edgeStoreFixture(t)
	if _, err := s.DB().Exec(`DELETE FROM edge_bindings; DELETE FROM edge_input_policy; DELETE FROM local_changes WHERE entity='edge_binding';
		CREATE TRIGGER reject_policy_audit BEFORE INSERT ON local_changes WHEN NEW.entity='edge_binding' BEGIN SELECT RAISE(ABORT,'fixture audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEdgeBindings(t.Context(), "100", nil); err == nil {
		t.Fatal("failed audit accepted restriction")
	}
	if allowed, err := s.AutomaticFeibotInput(t.Context(), "100"); err != nil || !allowed {
		t.Fatal("failed transaction changed automatic policy")
	}
}
