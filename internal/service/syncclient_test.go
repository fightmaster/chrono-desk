package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPushSyncSendsTokenAndParsesSummary(t *testing.T) {
	var gotToken, gotPath, gotBody string
	var capabilityChecks int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-SYNC-TOKEN")
		gotPath = r.URL.Path
		if r.URL.Path == "/api/sync/events/936919/capabilities" {
			capabilityChecks++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"push_schema_versions":[1,2,3],"preferred_push_schema_version":3}`))
			return
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"rfid_upserted":1,"observation_ack":{"batch_id":"batch-1","origin_instance_id":"desk-1","accepted_through_sequence":1,"items":[{"id":"log-1","origin_sequence":1,"status":"inserted"}]}}`))
	}))
	defer srv.Close()

	resp, err := PushSync(context.Background(), srv.URL+"/", "secret-token", "936919", []byte(`{"schema_version":3}`))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if gotToken != "secret-token" {
		t.Errorf("token header = %q", gotToken)
	}
	if gotPath != "/api/sync/events/936919" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody != `{"schema_version":3}` {
		t.Errorf("body = %q", gotBody)
	}
	if capabilityChecks != 1 {
		t.Errorf("capability checks = %d, want 1", capabilityChecks)
	}
	if resp.Summary["rfid_upserted"].(float64) != 1 || resp.ObservationAck == nil || resp.ObservationAck.Items[0].Status != "inserted" {
		t.Errorf("summary = %+v", resp)
	}
}

func TestPushSyncMapsErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_, _ = w.Write([]byte(`{"push_schema_versions":[3],"preferred_push_schema_version":3}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"токен не подходит к событию"}`))
	}))
	defer srv.Close()

	_, err := PushSync(context.Background(), srv.URL, "bad", "936919", []byte(`{"schema_version":3}`))
	if err == nil || !strings.Contains(err.Error(), "токен не подходит") {
		t.Fatalf("err = %v, want forbidden message", err)
	}
}

func TestPushSyncFailsClosedWhenSiteDoesNotSupportV3(t *testing.T) {
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
		}
		_, _ = w.Write([]byte(`{"push_schema_versions":[1],"preferred_push_schema_version":1}`))
	}))
	defer srv.Close()

	_, err := PushSync(context.Background(), srv.URL, "token", "936919", []byte(`{"schema_version":3}`))
	if err == nil || !strings.Contains(err.Error(), "v3") {
		t.Fatalf("err = %v, want unsupported v3", err)
	}
	if posts != 0 {
		t.Fatalf("posts = %d, unsafe payload must not be sent", posts)
	}
}

func TestSyncURLRequiresBase(t *testing.T) {
	if _, err := syncURL("  ", "936919"); err == nil {
		t.Error("empty base url must error")
	}
}

func TestPullChangeFeedPageUsesOpaqueCursorAndParsesObservations(t *testing.T) {
	var gotToken, gotAfter, gotLimit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-SYNC-TOKEN")
		gotAfter = r.URL.Query().Get("after")
		gotLimit = r.URL.Query().Get("limit")
		_, _ = w.Write([]byte(`{"schema_version":1,"items":[{"type":"observation_created","observation":{"id":"remote-1","event_id":"936919","observation_version":1,"capture_source_id":"stopwatch:start","origin_system":"stopwatch","origin_instance_id":"watch-1","origin_sequence":4,"status":0,"number":42,"time_ms":1780000000000,"ant":1,"epc":"","rssi":-50,"board":"start","disabled_at":null}}],"next_cursor":"opaque.next+value","has_more":false}`))
	}))
	defer srv.Close()

	page, err := PullChangeFeedPage(context.Background(), srv.URL, "secret", "936919", "opaque/previous+value", 500)
	if err != nil {
		t.Fatal(err)
	}
	if gotToken != "secret" || gotAfter != "opaque/previous+value" || gotLimit != "500" {
		t.Fatalf("request token=%q after=%q limit=%q", gotToken, gotAfter, gotLimit)
	}
	if page.SchemaVersion != 1 || page.NextCursor != "opaque.next+value" || len(page.Items) != 1 {
		t.Fatalf("page = %+v", page)
	}
	if got := page.Items[0].Observation; got.ID != "remote-1" || got.OriginInstanceID != "watch-1" || got.TimeMs != 1780000000000 {
		t.Fatalf("observation = %+v", got)
	}
}

func TestCapabilitiesExposeChangeFeedSupport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"push_schema_versions":[1,2,3],"preferred_push_schema_version":3,"change_feed_schema_versions":[1],"preferred_change_feed_schema_version":1}`))
	}))
	defer srv.Close()

	capabilities, err := GetSyncCapabilities(context.Background(), srv.URL, "secret", "936919")
	if err != nil {
		t.Fatal(err)
	}
	if !SupportsChangeFeed(capabilities, 1) || SupportsChangeFeed(capabilities, 2) {
		t.Fatalf("capabilities = %+v", capabilities)
	}
}
