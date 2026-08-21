package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

func TestConcurrentPullAllAndPushOwnPreservesOwnershipAndCursors(t *testing.T) {
	fixture, err := os.ReadFile("../../service/testdata/event-export.json")
	if err != nil {
		t.Fatal(err)
	}

	pushReceived := make(chan struct{})
	feedServed := make(chan struct{})
	releasePushAck := make(chan struct{})
	defer func() {
		select {
		case <-releasePushAck:
		default:
			close(releasePushAck)
		}
	}()
	type pushedItem struct {
		ID             string `json:"id"`
		OriginSequence int64  `json:"origin_sequence"`
	}
	var pushed struct {
		ObservationBatch *struct {
			BatchID          string       `json:"batch_id"`
			OriginInstanceID string       `json:"origin_instance_id"`
			LastSequence     int64        `json:"last_sequence"`
			Items            []pushedItem `json:"items"`
		} `json:"observation_batch"`
	}

	run5 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-SYNC-TOKEN") != "sync-token" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/capabilities"):
			_, _ = io.WriteString(w, `{"push_schema_versions":[3],"preferred_push_schema_version":3,"change_feed_schema_versions":[1],"preferred_change_feed_schema_version":1}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/sync/events/ev-100":
			if err := json.NewDecoder(r.Body).Decode(&pushed); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			close(pushReceived)
			<-releasePushAck
			if pushed.ObservationBatch == nil {
				http.Error(w, "missing observation batch", http.StatusUnprocessableEntity)
				return
			}
			items := make([]map[string]any, 0, len(pushed.ObservationBatch.Items))
			for _, item := range pushed.ObservationBatch.Items {
				items = append(items, map[string]any{
					"id": item.ID, "origin_sequence": item.OriginSequence, "status": "inserted",
				})
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"observation_ack": map[string]any{
					"batch_id":                  pushed.ObservationBatch.BatchID,
					"origin_instance_id":        pushed.ObservationBatch.OriginInstanceID,
					"accepted_through_sequence": pushed.ObservationBatch.LastSequence,
					"items":                     items,
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/sync/events/ev-100":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(fixture)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/changes"):
			close(feedServed)
			_, _ = io.WriteString(w, `{"schema_version":1,"items":[{"type":"observation_created","observation":{"id":"remote-watch-1","event_id":"ev-100","observation_version":1,"capture_source_id":"stopwatch:split","origin_system":"stopwatch","origin_instance_id":"watch-2","origin_sequence":9,"status":0,"number":0,"time_ms":1780812650000,"ant":1,"epc":"REMOTE-EPC","rssi":-48,"board":"remote-split","disabled_at":null}}],"next_cursor":"field-cursor-1","has_more":false}`)
		default:
			http.NotFound(w, r)
		}
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
	if _, err := store.InsertOwnedRfidLogs(ctx, []domain.RfidLog{{
		ID: "local-desk-1", EventID: "ev-100", TimeMs: 1780812660000,
		EPC: "LOCAL-EPC", Ant: 1, RSSI: -51, Board: "Feibot:U700",
		CaptureSourceID: "chrono-desk:ev-100:Feibot:U700",
	}}); err != nil {
		t.Fatal(err)
	}

	type requestResult struct {
		status int
		body   string
		err    error
	}
	request := func(path string) requestResult {
		req, err := http.NewRequest(http.MethodPost, srv.BaseURL()+path, strings.NewReader(`{}`))
		if err != nil {
			return requestResult{err: err}
		}
		req.Header.Set("Authorization", "Bearer "+testAPIToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return requestResult{err: err}
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		return requestResult{status: resp.StatusCode, body: string(body), err: err}
	}

	pushResult := make(chan requestResult, 1)
	go func() { pushResult <- request("/api/events/ev-100/sync") }()
	waitSignal(t, pushReceived, "push payload")

	pullResult := make(chan requestResult, 1)
	go func() { pullResult <- request("/api/events/ev-100/sync-pull") }()
	waitSignal(t, feedServed, "change feed")
	close(releasePushAck)

	assertRequestOK(t, "push", <-pushResult)
	assertRequestOK(t, "pull", <-pullResult)

	if pushed.ObservationBatch == nil || len(pushed.ObservationBatch.Items) != 1 || pushed.ObservationBatch.Items[0].ID != "local-desk-1" {
		t.Fatalf("push-own batch = %+v, want only local-desk-1", pushed.ObservationBatch)
	}
	var localState string
	if err := store.DB().QueryRow(`SELECT state FROM observation_outbox WHERE observation_id='local-desk-1'`).Scan(&localState); err != nil || localState != "acked" {
		t.Fatalf("local outbox state=%q err=%v, want acked", localState, err)
	}
	var localLogs, remoteLogs, remoteOutbox int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM rfid_logs WHERE id='local-desk-1'`).Scan(&localLogs); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM rfid_logs WHERE id='remote-watch-1'`).Scan(&remoteLogs); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM observation_outbox WHERE observation_id='remote-watch-1'`).Scan(&remoteOutbox); err != nil {
		t.Fatal(err)
	}
	if localLogs != 1 || remoteLogs != 1 || remoteOutbox != 0 {
		t.Fatalf("observations local=%d remote=%d remote-outbox=%d, want 1/1/0", localLogs, remoteLogs, remoteOutbox)
	}
	cursor, err := store.GetPullCursor(ctx, "ev-100")
	if err != nil || cursor == nil || *cursor != "field-cursor-1" {
		t.Fatalf("pull cursor=%v err=%v", cursor, err)
	}
}

func waitSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func assertRequestOK(t *testing.T, name string, result struct {
	status int
	body   string
	err    error
}) {
	t.Helper()
	if result.err != nil || result.status != http.StatusOK {
		t.Fatalf("%s result status=%d err=%v body=%s", name, result.status, result.err, result.body)
	}
}
