package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/rfid-core/edge"
	"gitlab.com/fightmaster1/rfid-core/ingest"
)

func TestEdgeLiveThenHistoricalPlateSiteImportKeepsOneFact(t *testing.T) {
	for _, path := range []string{"http_feed", "export_json"} {
		for _, state := range []string{"duplicate", "disable", "reenable"} {
			t.Run(path+"/"+state, func(t *testing.T) {
				ctx := context.Background()
				store, event := edgeLiveFixture(t)
				event.Board = "plate-test"
				event.ID = ingest.RFIDReadID(event.Board, event.EPC, event.Time, event.Ant)
				event.OriginSystem, event.CaptureSourceID = "edge", "plate-installation"
				event.ClockQuality = edge.ClockOperator
				if err := store.UpsertCheckpoint(ctx, domain.Checkpoint{ID: "finish", EventID: "100", RaceID: "race", Board: event.Board, Type: domain.CheckpointType(3), Sort: 1}); err != nil {
					t.Fatal(err)
				}
				if err := store.SetEdgeBindings(ctx, "100", []domain.EdgeBinding{{Board: event.Board, SourceSessionID: event.SourceSessionID}}); err != nil {
					t.Fatal(err)
				}
				if ack, err := edgeRoundTrip(t, store, event, false); err != nil || ack != string(edge.ACK(event)) {
					t.Fatalf("initial source acceptance: ack=%q err=%v", ack, err)
				}
				if state == "reenable" {
					if _, err := store.DB().Exec(`UPDATE rfid_logs SET disabled_at=1000 WHERE id=?`, event.ID); err != nil {
						t.Fatal(err)
					}
				}
				original, err := store.ListRfidLogs(ctx, "100")
				if err != nil || len(original) != 1 {
					t.Fatalf("original raw=%+v err=%v", original, err)
				}
				journal, err := store.EdgeJournal(ctx, "100", 0, 10)
				if err != nil || len(journal) != 1 {
					t.Fatalf("original source journal=%+v err=%v", journal, err)
				}
				// Old plate supplies its antenna-less ID and no edge metadata.
				// Its stored EPC case and receiver status/RSSI can also differ.
				legacyEPC := strings.ToLower(event.EPC)
				legacyID := ingest.LegacyPlateReadID(event.Board, legacyEPC, event.Time)
				if legacyID == event.ID {
					t.Fatal("fixture does not exercise different IDs")
				}
				row := map[string]any{"id": legacyID, "event_id": "100", "board": event.Board,
					"epc": legacyEPC, "time": event.Time, "time_ms": event.Time,
					"ant": event.Ant, "number": event.Number, "status": 1, "rssi": -90, "disabled_at": nil}
				original[0].DisabledAt = nil
				if state == "disable" {
					disabled := event.Time + 1000
					row["disabled_at"] = time.UnixMilli(disabled).UTC().Format(time.RFC3339Nano)
					original[0].DisabledAt = &disabled
				}
				if path == "http_feed" {
					body, err := json.Marshal(map[string]any{"schema_version": 1, "next_cursor": "legacy-cursor", "has_more": false,
						"items": []any{map[string]any{"type": "observation_state_changed", "observation": row}}})
					if err != nil {
						t.Fatal(err)
					}
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.Header.Get("X-SYNC-TOKEN") != "fixture-token" {
							http.Error(w, "missing token", http.StatusUnauthorized)
							return
						}
						if r.URL.Path == "/api/sync/events/100/capabilities" {
							_, _ = w.Write([]byte(`{"change_feed_schema_versions":[1]}`))
							return
						}
						_, _ = w.Write(body)
					}))
					defer server.Close()
					stats, err := PullEventChanges(ctx, store, server.URL, "fixture-token", "100", time.UnixMilli(event.Time+2000))
					if err != nil || stats.Inserted != 0 || stats.Observations != 1 || stats.Recovery {
						t.Fatalf("legacy HTTP import=%+v err=%v", stats, err)
					}
					if state == "duplicate" {
						if stats.Duplicates != 1 || stats.StateChanges != 0 || stats.Plan.ReplayEvent || len(stats.Plan.Races) != 0 || len(stats.Plan.Members) != 0 {
							t.Fatalf("duplicate scheduled timing work: %+v", stats)
						}
					} else if stats.StateChanges != 1 || stats.Duplicates != 0 || stats.Plan.ReplayEvent || len(stats.Plan.Races) != 0 || len(stats.Plan.Members) != 1 || stats.Plan.Members[0].MemberID != "member" {
						t.Fatalf("judge change missed the existing member: %+v", stats)
					}
					if cursor, err := store.GetPullCursor(ctx, "100"); err != nil || cursor == nil || *cursor != "legacy-cursor" {
						t.Fatalf("cursor=%v err=%v", cursor, err)
					}
				} else {
					body, err := json.Marshal(map[string]any{"schema_version": 3, "timezone": "UTC",
						"event": map[string]string{"id": "100", "name": "Synthetic edge live"}, "rfid_logs": []any{row}})
					if err != nil {
						t.Fatal(err)
					}
					export, err := ParseEventExport(bytes.NewReader(body))
					if err != nil {
						t.Fatal(err)
					}
					if _, err := NewEventImporter(store).Import(ctx, export); err != nil {
						t.Fatal(err)
					}
				}
				stored, err := store.ListRfidLogs(ctx, "100")
				if err != nil || !reflect.DeepEqual(stored, original) {
					t.Fatalf("import duplicated/replaced local fact: got=%+v want=%+v err=%v", stored, original, err)
				}
				after, err := store.EdgeJournal(ctx, "100", 0, 10)
				if err != nil || !reflect.DeepEqual(after, journal) {
					t.Fatalf("site changed the original source relay: %+v err=%v", after, err)
				}
				batch, err := store.PrepareObservationBatch(ctx, "100", 100, time.Now())
				if err != nil || batch != nil {
					t.Fatalf("site import acquired native v3 ownership: %+v err=%v", batch, err)
				}
			})
		}
	}
}
