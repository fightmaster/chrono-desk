package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

func TestPushDrainsOverTenThousandOwnedObservationsInBoundedAcknowledgedBatches(t *testing.T) {
	fixture, err := os.ReadFile("../../service/testdata/event-export.json")
	if err != nil {
		t.Fatal(err)
	}
	requests, received := 0, 0
	run5 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			io.WriteString(w, `{"push_schema_versions":[3],"preferred_push_schema_version":3}`)
			return
		}
		var payload struct {
			Batch *struct {
				BatchID string `json:"batch_id"`
				Origin  string `json:"origin_instance_id"`
				Last    int64  `json:"last_sequence"`
				Items   []struct {
					ID       string `json:"id"`
					Sequence int64  `json:"origin_sequence"`
				} `json:"items"`
			} `json:"observation_batch"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			http.Error(w, "decode", 400)
			return
		}
		if payload.Batch == nil || len(payload.Batch.Items) > 1000 {
			t.Errorf("unbounded or missing batch: %+v", payload.Batch)
			http.Error(w, "batch", 400)
			return
		}
		requests++
		received += len(payload.Batch.Items)
		items := make([]map[string]any, 0, len(payload.Batch.Items))
		for _, item := range payload.Batch.Items {
			if !strings.HasPrefix(item.ID, "backlog-") {
				t.Errorf("imported observation was repushed: %s", item.ID)
			}
			items = append(items, map[string]any{"id": item.ID, "origin_sequence": item.Sequence, "status": "inserted"})
		}
		writeJSON(w, 200, map[string]any{"observation_ack": map[string]any{"batch_id": payload.Batch.BatchID, "origin_instance_id": payload.Batch.Origin, "accepted_through_sequence": payload.Batch.Last, "items": items}})
	}))
	defer run5.Close()
	srv := startTestServer(t)
	decodeBody(t, mustPost(t, srv.BaseURL()+"/api/events/import", "application/json", bytes.NewReader(fixture)), &map[string]any{})
	store, err := srv.events.Open("ev-100")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.SetSyncConfig(ctx, "ev-100", run5.URL, "sync-token"); err != nil {
		t.Fatal(err)
	}
	logs := make([]domain.RfidLog, 10001)
	for i := range logs {
		logs[i] = domain.RfidLog{ID: fmt.Sprintf("backlog-%05d", i), EventID: "ev-100", Board: "B1", TimeMs: int64(1780813000000 + i), CaptureSourceID: "chrono-desk:ev-100:B1"}
	}
	if _, err := store.InsertOwnedRfidLogs(ctx, logs); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 11; i++ {
		var response struct {
			HasMore   bool `json:"has_more"`
			Remaining int  `json:"remaining_observations"`
		}
		decodeBody(t, mustPost(t, srv.BaseURL()+"/api/events/ev-100/sync", "application/json", strings.NewReader(`{}`)), &response)
		want := 10001 - (i+1)*1000
		if want < 0 {
			want = 0
		}
		if response.Remaining != want || response.HasMore != (want > 0) {
			t.Fatalf("batch %d: remaining=%d has_more=%v want=%d", i, response.Remaining, response.HasMore, want)
		}
	}
	if requests != 11 || received != 10001 {
		t.Fatalf("requests=%d received=%d", requests, received)
	}
	var acked int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM observation_outbox WHERE state='acked'`).Scan(&acked); err != nil || acked != 10001 {
		t.Fatalf("acked=%d err=%v", acked, err)
	}
}
