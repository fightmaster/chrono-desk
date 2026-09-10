package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

func edgeExportFixture(t *testing.T) (*EventExport, map[string]any) {
	t.Helper()
	payload, err := os.ReadFile("testdata/edge-observation-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(payload)); got != "952e179f068c3027da3232f46bcd81b525aa3490580e741ed4d93015c670c83d" {
		t.Fatalf("core fixture checksum: %s", got)
	}
	var row map[string]any
	if err := json.Unmarshal(payload, &row); err != nil {
		t.Fatal(err)
	}
	row["event_id"] = "230557"
	document := map[string]any{"schema_version": 3, "timezone": "UTC", "event": map[string]string{"id": "230557", "name": "Fixture"}, "rfid_logs": []any{row}}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	export, err := ParseEventExport(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	return export, row
}

func TestEdgeMetadataImportPreservesOriginAndNeverClaimsOwnership(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	export, _ := edgeExportFixture(t)
	for i := 0; i < 2; i++ {
		if _, err := NewEventImporter(store).Import(ctx, export); err != nil {
			t.Fatal(err)
		}
	}
	logs, err := store.ListRfidLogs(ctx, "230557")
	if err != nil || len(logs) != 1 {
		t.Fatalf("logs=%v err=%v", logs, err)
	}
	if logs[0].OriginInstanceID != "installation-test" || logs[0].OriginSequence != 42 {
		t.Fatalf("origin lost: %+v", logs[0])
	}
	var session, clock string
	if err := store.DB().QueryRow(`SELECT source_session_id,clock_evidence_id FROM rfid_logs`).Scan(&session, &clock); err != nil {
		t.Fatal(err)
	}
	if session != "session-test" || clock != "clock-test" {
		t.Fatalf("metadata=%q/%q", session, clock)
	}
	for _, table := range []string{"observation_outbox", "edge_observation_outbox"} {
		var count int
		if err := store.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s=%d err=%v", table, count, err)
		}
	}
}

func TestEdgeMetadataImportRejectsInvalidEnvelopeBeforeWriting(t *testing.T) {
	for _, patch := range []map[string]any{
		{"edge_version": 2}, {"edge_version": nil}, {"source_session_id": ""},
		{"clock_quality": "guessed"}, {"clock_evidence_id": nil},
		{"origin_sequence": -1}, {"id": "changed-id"}, {"event_id": "230558"},
	} {
		t.Run(fmt.Sprint(patch), func(t *testing.T) {
			store := newTestStore(t)
			export, row := edgeExportFixture(t)
			for key, value := range patch {
				row[key] = value
			}
			encoded, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			export.RfidLogs[0] = exportRfidLog{}
			if err := json.Unmarshal(encoded, &export.RfidLogs[0]); err != nil {
				t.Fatal(err)
			}
			if _, err := NewEventImporter(store).Import(context.Background(), export); err == nil {
				t.Fatal("invalid edge metadata was accepted")
			}
			var count int
			if err := store.DB().QueryRow(`SELECT COUNT(*) FROM events`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("partial event=%d err=%v", count, err)
			}
		})
	}
}

func TestEdgeMetadataFeedRejectsUnsupportedAndPartialInsteadOfLegacyFallback(t *testing.T) {
	_, row := edgeExportFixture(t)
	row["time_ms"] = row["time"]
	for _, field := range []string{"edge_version", "source_session_id", "identity_profile", "clock_evidence_id", "clock_quality"} {
		t.Run(field, func(t *testing.T) {
			copyRow := make(map[string]any, len(row))
			for key, value := range row {
				copyRow[key] = value
			}
			copyRow[field] = nil
			encoded, _ := json.Marshal(copyRow)
			var input ChangeFeedObservation
			if err := json.Unmarshal(encoded, &input); err != nil {
				t.Fatal(err)
			}
			if _, err := feedObservation(input); err == nil {
				t.Fatal("partial edge metadata was silently accepted")
			}
		})
	}
	// A marker with an explicit null value is still an edge envelope.
	var input ChangeFeedObservation
	if err := json.Unmarshal([]byte(`{"id":"native","event_id":"ev1","board":"finish","edge_version":null}`), &input); err != nil {
		t.Fatal(err)
	}
	if _, err := feedObservation(input); err == nil {
		t.Fatal("null marker was downgraded")
	}
	if _, err := feedObservation(ChangeFeedObservation{ID: "native", EventID: "ev1", Board: "finish"}); err != nil {
		t.Fatalf("legacy rejected: %v", err)
	}
}

func TestEdgeMetadataHTTPFeedKeepsCommittedCursorWhenNextPageIsInvalid(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	if err := store.UpsertEvent(ctx, domain.Event{ID: "230557", Name: "Fixture"}); err != nil {
		t.Fatal(err)
	}
	_, row := edgeExportFixture(t)
	row["time_ms"] = row["time"]
	good, err := json.Marshal(map[string]any{"schema_version": 1, "next_cursor": "good", "has_more": true, "items": []any{map[string]any{"type": "observation_created", "observation": row}}})
	if err != nil {
		t.Fatal(err)
	}
	row["edge_version"] = 2
	bad, err := json.Marshal(map[string]any{"schema_version": 1, "next_cursor": "bad", "has_more": false, "items": []any{map[string]any{"type": "observation_created", "observation": row}}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-SYNC-TOKEN") != "test-token" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/api/sync/events/230557/capabilities" {
			_, _ = w.Write([]byte(`{"change_feed_schema_versions":[1]}`))
			return
		}
		if r.URL.Query().Get("after") == "" {
			_, _ = w.Write(good)
		} else {
			_, _ = w.Write(bad)
		}
	}))
	defer server.Close()
	if _, err := PullEventChanges(ctx, store, server.URL, "test-token", "230557", time.UnixMilli(1234)); err == nil {
		t.Fatal("unsupported second page accepted")
	}
	cursor, err := store.GetPullCursor(ctx, "230557")
	if err != nil || cursor == nil || *cursor != "good" {
		t.Fatalf("cursor=%v err=%v", cursor, err)
	}
	logs, err := store.ListRfidLogs(ctx, "230557")
	if err != nil || len(logs) != 1 || logs[0].EdgeMetadata == nil || logs[0].ClockEvidenceID != "clock-test" {
		t.Fatalf("committed page=%+v err=%v", logs, err)
	}
	for _, table := range []string{"observation_outbox", "edge_observation_outbox"} {
		var count int
		if err := store.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("echo outbox %s=%d err=%v", table, count, err)
		}
	}
}
