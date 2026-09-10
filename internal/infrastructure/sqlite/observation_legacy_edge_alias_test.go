package sqlite

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/rfid-core/ingest"
)

func historicalPlateLog(ev ingest.Event) domain.RfidLog {
	// This is the actual old plate identity and native site shape, not an
	// invented ID or an edge envelope with its provenance silently removed.
	return domain.RfidLog{ID: ingest.LegacyPlateReadID(ev.Board, ev.EPC, ev.Time),
		EventID: "100", Board: ev.Board, EPC: ev.EPC, TimeMs: ev.Time,
		Ant: ev.Ant, Number: ev.Number, RSSI: -90, Status: 1}
}

func TestHistoricalNativeImportAfterEdgeKeepsOneFactAndSourceJournal(t *testing.T) {
	for _, path := range []string{"feed", "snapshot"} {
		for _, disable := range []bool{false, true} {
			name := path + "/duplicate"
			if disable {
				name = path + "/site_disable"
			}
			t.Run(name, func(t *testing.T) {
				s, ev := edgeStoreFixture(t)
				ctx := context.Background()
				if _, err := acceptEdge(s, "100", ev); err != nil {
					t.Fatal(err)
				}
				before, _, err := s.findRfidLog(ctx, ev.ID)
				if err != nil {
					t.Fatal(err)
				}
				journal, err := s.EdgeJournal(ctx, "100", 0, 10)
				if err != nil || len(journal) != 1 {
					t.Fatalf("initial source journal: %+v %v", journal, err)
				}
				fence, err := s.ProjectionFenceEvidence(ctx, "100")
				if err != nil {
					t.Fatal(err)
				}
				incoming := historicalPlateLog(ev)
				if incoming.ID == ev.ID {
					t.Fatal("fixture must exercise distinct legacy and antenna-aware IDs")
				}
				if disable {
					disabled := int64(1234)
					incoming.DisabledAt = &disabled
					before.DisabledAt = &disabled
				}
				for attempt := 0; attempt < 2; attempt++ {
					if path == "feed" {
						mutations, err := s.ApplyObservationFeedPageWithMutations(ctx, "100", []domain.RfidLog{incoming}, "legacy-cursor", 2000)
						want := ObservationFeedDuplicate
						if disable && attempt == 0 {
							want = ObservationFeedStateChanged
						}
						if err != nil || len(mutations) != 1 || mutations[0].Kind != want || mutations[0].Observation.ID != ev.ID {
							t.Fatalf("legacy alias mutation=%+v want kind=%v local ID=%s err=%v", mutations, want, ev.ID, err)
						}
					} else if err := s.UpsertRfidLogs(ctx, []domain.RfidLog{incoming}); err != nil {
						t.Fatal(err)
					}
					if count, err := s.CountRfidLogs(ctx, "100"); err != nil || count != 1 {
						t.Fatalf("historical feed/snapshot duplicated an edge fact: count=%d err=%v", count, err)
					}
					stored, found, err := s.findRfidLog(ctx, ev.ID)
					if err != nil || !found || !reflect.DeepEqual(stored, before) {
						t.Fatalf("local source fact changed: got=%+v want=%+v err=%v", stored, before, err)
					}
					if _, found, err := s.findRfidLog(ctx, incoming.ID); err != nil || found {
						t.Fatal("historical alias created another local ID")
					}
					after, err := s.EdgeJournal(ctx, "100", 0, 10)
					if err != nil || !reflect.DeepEqual(after, journal) {
						t.Fatal("site import changed the pending source relay envelope")
					}
					if !disable {
						afterFence, err := s.ProjectionFenceEvidence(ctx, "100")
						// The first feed page legitimately advances cursor evidence.
						// A snapshot or repeat of that same cursor must not change it.
						if err != nil || ((path == "snapshot" || attempt > 0) && !reflect.DeepEqual(fence, afterFence)) {
							t.Fatalf("duplicate changed timing evidence: %+v / %+v err=%v", fence, afterFence, err)
						}
						fence = afterFence
					}
					var owned int
					if err := s.DB().QueryRow(`SELECT COUNT(*) FROM observation_outbox`).Scan(&owned); err != nil || owned != 0 {
						t.Fatal("site import became Desk-owned")
					}
				}
			})
		}
	}
}

func TestHistoricalNativeImportRejectsAmbiguousEdgeAliasesAtomically(t *testing.T) {
	for _, nativeCount := range []int{1, 3} {
		t.Run(strconv.Itoa(nativeCount), func(t *testing.T) {
			s, ev := edgeStoreFixture(t)
			ctx := context.Background()
			// A second historical, case-preserved legacy ID is ambiguous. Sorting by
			// edge presence must not hide it or collapse unrelated native-only history.
			for _, epc := range []string{strings.ToLower(ev.EPC), "e280AABB", "E280aabb"}[:nativeCount] {
				legacy := historicalPlateLog(ev)
				legacy.EPC = epc
				legacy.ID = ingest.LegacyPlateReadID(ev.Board, legacy.EPC, ev.Time)
				if _, err := s.InsertRfidLogs(ctx, []domain.RfidLog{legacy}); err != nil {
					t.Fatal(err)
				}
			}
			// Seed an already ambiguous historical store, with edge last so a
			// LIMIT without edge-first ordering can hide it behind native rows.
			if _, err := s.InsertRfidLogs(ctx, []domain.RfidLog{edgeMetadataLog(ev)}); err != nil {
				t.Fatal(err)
			}
			incoming := historicalPlateLog(ev)
			fresh := incoming
			fresh.ID, fresh.TimeMs = "fresh-native-item", ev.Time+1
			if err := s.ApplyObservationFeedPage(ctx, "100", []domain.RfidLog{fresh, incoming}, "rejected-cursor", 3000); err == nil {
				t.Fatal("ambiguous native/edge aliases accepted")
			}
			if cursor, err := s.GetPullCursor(ctx, "100"); err != nil || cursor != nil {
				t.Fatal("ambiguous page advanced its cursor")
			}
			if count, err := s.CountRfidLogs(ctx, "100"); err != nil || count != int64(nativeCount+1) {
				t.Fatalf("failed page left a partial insert: count=%d err=%v", count, err)
			}
		})
	}
}

func TestHistoricalNativeImportDoesNotChangeNativeOnlyIdentityRules(t *testing.T) {
	s, ev := edgeStoreFixture(t)
	ctx := context.Background()
	for _, epc := range []string{"e280aabb", "e280AABB"} {
		legacy := historicalPlateLog(ev)
		legacy.EPC = epc
		legacy.ID = ingest.LegacyPlateReadID(ev.Board, epc, ev.Time)
		if _, err := s.InsertRfidLogs(ctx, []domain.RfidLog{legacy}); err != nil {
			t.Fatal(err)
		}
	}
	incoming := historicalPlateLog(ev)
	mutations, err := s.ApplyObservationFeedPageWithMutations(ctx, "100", []domain.RfidLog{incoming}, "native-cursor", 2000)
	if err != nil || len(mutations) != 1 || mutations[0].Kind != ObservationFeedInserted {
		t.Fatalf("native-only import unexpectedly aliased/rejected: %+v %v", mutations, err)
	}
	if count, err := s.CountRfidLogs(ctx, "100"); err != nil || count != 3 {
		t.Fatalf("native history changed: count=%d err=%v", count, err)
	}
}

func TestHistoricalNativeImportCannotDowngradeStoredInvalidEdgeAlias(t *testing.T) {
	for _, patch := range []string{
		"edge_version=2",
		"edge_version=1",
		"source_session_id='partial'",
		"identity_profile='rfid-v1'",
		"clock_evidence_id='partial'",
		"clock_quality='operator_confirmed'",
	} {
		for _, path := range []string{"feed", "snapshot"} {
			t.Run(path+"/"+patch, func(t *testing.T) {
				s, ev := edgeStoreFixture(t)
				ctx := context.Background()
				if _, err := acceptEdge(s, "100", ev); err != nil {
					t.Fatal(err)
				}
				if patch != "edge_version=2" {
					if _, err := s.DB().Exec(`UPDATE rfid_logs SET edge_version=NULL,source_session_id=NULL,identity_profile=NULL,clock_evidence_id=NULL,clock_quality=NULL`); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := s.DB().Exec(`UPDATE rfid_logs SET ` + patch); err != nil {
					t.Fatal(err)
				}
				incoming := historicalPlateLog(ev)
				fresh := incoming
				fresh.ID, fresh.TimeMs = "fresh-native-item", ev.Time+1
				var err error
				if path == "feed" {
					err = s.ApplyObservationFeedPage(ctx, "100", []domain.RfidLog{fresh, incoming}, "bad", 2000)
				} else {
					err = s.UpsertRfidLogs(ctx, []domain.RfidLog{fresh, incoming})
				}
				if err == nil {
					t.Fatal("invalid stored edge alias silently downgraded")
				}
				if count, err := s.CountRfidLogs(ctx, "100"); err != nil || count != 1 {
					t.Fatalf("partial import: count=%d err=%v", count, err)
				}
				if cursor, err := s.GetPullCursor(ctx, "100"); err != nil || cursor != nil {
					t.Fatal("invalid page advanced cursor")
				}
			})
		}
	}
}

func TestHistoricalNativeImportDoesNotAliasDifferentPhysicalFacts(t *testing.T) {
	for name, change := range map[string]func(*domain.RfidLog){
		"event":      func(v *domain.RfidLog) { v.EventID = "200" },
		"board case": func(v *domain.RfidLog) { v.Board = strings.ToUpper(v.Board) },
		"time":       func(v *domain.RfidLog) { v.TimeMs++ },
		"antenna":    func(v *domain.RfidLog) { v.Ant++ },
		"number":     func(v *domain.RfidLog) { v.Number++ },
		"epc":        func(v *domain.RfidLog) { v.EPC += "F" },
	} {
		t.Run(name, func(t *testing.T) {
			s, ev := edgeStoreFixture(t)
			ctx := context.Background()
			if _, err := acceptEdge(s, "100", ev); err != nil {
				t.Fatal(err)
			}
			if err := s.UpsertEvent(ctx, domain.Event{ID: "200", Name: "Other event"}); err != nil {
				t.Fatal(err)
			}
			incoming := historicalPlateLog(ev)
			change(&incoming)
			incoming.ID = ingest.LegacyPlateReadID(incoming.Board, incoming.EPC, incoming.TimeMs)
			if err := s.UpsertRfidLogs(ctx, []domain.RfidLog{incoming}); err != nil {
				t.Fatal(err)
			}
			stored, found, err := s.findRfidLog(ctx, incoming.ID)
			if err != nil || !found || !reflect.DeepEqual(stored, incoming) {
				t.Fatalf("distinct native fact was lost: %+v %v", stored, err)
			}
			original, found, err := s.findRfidLog(ctx, ev.ID)
			if err != nil || !found || !reflect.DeepEqual(original, edgeMetadataLog(ev)) {
				t.Fatalf("original edge fact changed: %+v %v", original, err)
			}
		})
	}
}
