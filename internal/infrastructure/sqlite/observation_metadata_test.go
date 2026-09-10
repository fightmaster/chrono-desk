package sqlite

import (
	"context"
	"reflect"
	"strconv"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/rfid-core/ingest"
)

func edgeMetadataLog(event ingest.Event) domain.RfidLog {
	return domain.RfidLog{ID: event.ID, EventID: strconv.FormatInt(event.ExternalEventID, 10), Board: event.Board, EPC: event.EPC,
		TimeMs: event.Time, Number: event.Number, Ant: event.Ant, RSSI: event.RSSI, Status: event.Status,
		ObservationVersion: event.ObservationVersion, CaptureSourceID: event.CaptureSourceID, OriginSystem: event.OriginSystem,
		OriginInstanceID: event.OriginInstanceID, OriginSequence: int64(event.OriginSequence),
		EdgeMetadata: &domain.EdgeMetadata{EdgeVersion: event.EdgeVersion, SourceSessionID: event.SourceSessionID, IdentityProfile: event.IdentityProfile, ClockEvidenceID: event.ClockEvidenceID, ClockQuality: event.ClockQuality}}
}

func TestEdgeMetadataFeedPreservesLocalFirstWriterAndPendingWire(t *testing.T) {
	for _, firstWriter := range []string{"edge", "native"} {
		t.Run(firstWriter, func(t *testing.T) {
			s, ev := edgeStoreFixture(t)
			ctx := context.Background()
			if firstWriter == "edge" {
				if _, err := acceptEdge(s, "100", ev); err != nil {
					t.Fatal(err)
				}
			} else {
				WithOriginInstanceID("native-desk")(s)
				log := edgeMetadataLog(ev)
				log.EdgeMetadata = nil
				if _, err := s.InsertOwnedRfidLogs(ctx, []domain.RfidLog{log}); err != nil {
					t.Fatal(err)
				}
			}
			before, _, err := s.findRfidLog(ctx, ev.ID)
			if err != nil {
				t.Fatal(err)
			}
			journal, err := s.EdgeJournal(ctx, "100", 0, 10)
			if err != nil {
				t.Fatal(err)
			}
			incoming := edgeMetadataLog(ev)
			incoming.OriginInstanceID = "another-source"
			incoming.ClockEvidenceID = "another-clock"
			incoming.SourceSessionID = "another-session"
			incoming.RSSI = -90 // Link-dependent signal strength is not a new crossing.
			disabledAt := int64(1234)
			incoming.DisabledAt = &disabledAt
			mutations, err := s.ApplyObservationFeedPageWithMutations(ctx, "100", []domain.RfidLog{incoming}, "cursor-1", 10)
			if err != nil || len(mutations) != 1 || mutations[0].Kind != ObservationFeedStateChanged {
				t.Fatalf("mutations=%+v err=%v", mutations, err)
			}
			before.DisabledAt = &disabledAt
			stored, _, err := s.findRfidLog(ctx, ev.ID)
			if err != nil || !reflect.DeepEqual(stored, before) {
				t.Fatalf("first writer changed: got=%+v want=%+v err=%v", stored, before, err)
			}
			if !reflect.DeepEqual(mutations[0].Observation, stored) {
				t.Fatal("planner received a different source than stored")
			}
			after, err := s.EdgeJournal(ctx, "100", 0, 10)
			if err != nil || !reflect.DeepEqual(journal, after) {
				t.Fatal("feed changed pending relay wire")
			}
			if err := s.UpsertRfidLogs(ctx, []domain.RfidLog{incoming}); err != nil {
				t.Fatal(err)
			}
			stored, _, _ = s.findRfidLog(ctx, ev.ID)
			if !reflect.DeepEqual(stored, before) {
				t.Fatal("snapshot changed first writer")
			}
		})
	}
}

func TestEdgeMetadataImportKeepsHistoricalAliasAndRejectsAmbiguity(t *testing.T) {
	for _, aliases := range []int{1, 2} {
		t.Run(strconv.Itoa(aliases), func(t *testing.T) {
			s, ev := edgeStoreFixture(t)
			ctx := context.Background()
			for i := 0; i < aliases; i++ {
				legacy := edgeMetadataLog(ev)
				legacy.ID, legacy.EPC, legacy.EdgeMetadata = "legacy-"+strconv.Itoa(i), "e280aabb", nil
				legacy.OriginSystem = "site"
				if _, err := s.InsertRfidLogs(ctx, []domain.RfidLog{legacy}); err != nil {
					t.Fatal(err)
				}
			}
			incoming := edgeMetadataLog(ev)
			disabled := int64(1000)
			incoming.DisabledAt = &disabled
			err := s.ApplyObservationFeedPage(ctx, "100", []domain.RfidLog{incoming}, "next", 2000)
			if aliases == 2 {
				if err == nil {
					t.Fatal("ambiguous alias accepted")
				}
				if cursor, err := s.GetPullCursor(ctx, "100"); err != nil || cursor != nil {
					t.Fatal("cursor advanced on ambiguity")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				stored, _, err := s.findRfidLog(ctx, "legacy-0")
				if err != nil || stored.EdgeMetadata != nil || stored.OriginSystem != "site" || stored.DisabledAt == nil {
					t.Fatalf("alias lost original: %+v %v", stored, err)
				}
			}
			if count, err := s.CountRfidLogs(ctx, "100"); err != nil || count != int64(aliases) {
				t.Fatalf("duplicate count=%d err=%v", count, err)
			}
		})
	}
}

func TestEdgeMetadataInvalidLastItemRollsBackFeedAndSnapshot(t *testing.T) {
	for _, path := range []string{"feed", "snapshot"} {
		t.Run(path, func(t *testing.T) {
			s, ev := edgeStoreFixture(t)
			ctx := context.Background()
			good := edgeMetadataLog(ev)
			bad := edgeMetadataLog(ev)
			bad.EdgeVersion = 2
			var err error
			if path == "feed" {
				err = s.ApplyObservationFeedPage(ctx, "100", []domain.RfidLog{good, bad}, "bad", 1)
			} else {
				err = s.UpsertRfidLogs(ctx, []domain.RfidLog{good, bad})
			}
			if err == nil {
				t.Fatal("invalid envelope accepted")
			}
			if count, err := s.CountRfidLogs(ctx, "100"); err != nil || count != 0 {
				t.Fatalf("partial rows=%d err=%v", count, err)
			}
			if cursor, err := s.GetPullCursor(ctx, "100"); err != nil || cursor != nil {
				t.Fatal("partial cursor")
			}
		})
	}
}

func TestEdgeMetadataCannotEnterDeskOwnedOutbox(t *testing.T) {
	s, ev := edgeStoreFixture(t)
	WithOriginInstanceID("native-desk")(s)
	if _, err := s.InsertOwnedRfidLogs(context.Background(), []domain.RfidLog{edgeMetadataLog(ev)}); err == nil {
		t.Fatal("edge became Desk-owned")
	}
	if count, err := s.CountRfidLogs(context.Background(), "100"); err != nil || count != 0 {
		t.Fatal("rejected edge left raw row")
	}
}

func TestEdgeMetadataCannotRewriteNativeCaseOrStatusToFitEnvelope(t *testing.T) {
	s, ev := edgeStoreFixture(t)
	ctx := context.Background()
	native := edgeMetadataLog(ev)
	native.EdgeMetadata, native.Status, native.EPC = nil, 1, "e280aabb"
	if _, err := s.InsertRfidLogs(ctx, []domain.RfidLog{native}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertRfidLogs(ctx, []domain.RfidLog{edgeMetadataLog(ev)}); err != nil {
		t.Fatal(err)
	}
	stored, _, err := s.findRfidLog(ctx, native.ID)
	if err != nil || !reflect.DeepEqual(native, stored) {
		t.Fatalf("native changed: %+v %v", stored, err)
	}
}

func TestEdgeMetadataMigrationPreservesLegacyRowAndAddsNullableFields(t *testing.T) {
	s, ev := edgeStoreFixture(t)
	ctx := context.Background()
	legacy := edgeMetadataLog(ev)
	legacy.EdgeMetadata = nil
	if _, err := s.InsertRfidLogs(ctx, []domain.RfidLog{legacy}); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"edge_version", "source_session_id", "identity_profile", "clock_evidence_id", "clock_quality"} {
		if _, err := s.DB().Exec(`ALTER TABLE rfid_logs DROP COLUMN ` + column); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := New(s.DB()); err != nil {
			t.Fatal(err)
		}
	}
	stored, found, err := s.findRfidLog(ctx, legacy.ID)
	if err != nil || !found || !reflect.DeepEqual(stored, legacy) {
		t.Fatalf("legacy changed: %+v %v", stored, err)
	}
	if err := s.UpsertRfidLogs(ctx, []domain.RfidLog{edgeMetadataLog(ev)}); err != nil {
		t.Fatal(err)
	}
	stored, _, err = s.findRfidLog(ctx, legacy.ID)
	if err != nil || !reflect.DeepEqual(stored, edgeMetadataLog(ev)) {
		t.Fatalf("matching source metadata lost: %+v %v", stored, err)
	}
}

func TestEdgeMetadataOnlyEnrichmentDoesNotChangeProjectionFence(t *testing.T) {
	s, ev := edgeStoreFixture(t)
	ctx := context.Background()
	legacy := edgeMetadataLog(ev)
	legacy.EdgeMetadata = nil
	if _, err := s.InsertRfidLogs(ctx, []domain.RfidLog{legacy}); err != nil {
		t.Fatal(err)
	}
	before, err := s.ProjectionFenceEvidence(ctx, "100")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertRfidLogs(ctx, []domain.RfidLog{edgeMetadataLog(ev)}); err != nil {
		t.Fatal(err)
	}
	after, err := s.ProjectionFenceEvidence(ctx, "100")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("metadata-only timing fence change: %+v / %+v err=%v", before, after, err)
	}
}
