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
			_, _ = w.Write([]byte(`{"push_schema_versions":[1,2],"preferred_push_schema_version":2}`))
			return
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"rfid_inserted": 5, "recount_dispatched": true}`))
	}))
	defer srv.Close()

	resp, err := PushSync(context.Background(), srv.URL+"/", "secret-token", "936919", []byte(`{"hi":1}`))
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if gotToken != "secret-token" {
		t.Errorf("token header = %q", gotToken)
	}
	if gotPath != "/api/sync/events/936919" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody != `{"hi":1}` {
		t.Errorf("body = %q", gotBody)
	}
	if capabilityChecks != 1 {
		t.Errorf("capability checks = %d, want 1", capabilityChecks)
	}
	if resp["rfid_inserted"].(float64) != 5 {
		t.Errorf("summary = %+v", resp)
	}
}

func TestPushSyncMapsErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/capabilities") {
			_, _ = w.Write([]byte(`{"push_schema_versions":[2],"preferred_push_schema_version":2}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"токен не подходит к событию"}`))
	}))
	defer srv.Close()

	_, err := PushSync(context.Background(), srv.URL, "bad", "936919", []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "токен не подходит") {
		t.Fatalf("err = %v, want forbidden message", err)
	}
}

func TestPushSyncFailsClosedWhenSiteDoesNotSupportV2(t *testing.T) {
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
		}
		_, _ = w.Write([]byte(`{"push_schema_versions":[1],"preferred_push_schema_version":1}`))
	}))
	defer srv.Close()

	_, err := PushSync(context.Background(), srv.URL, "token", "936919", []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "v2") {
		t.Fatalf("err = %v, want unsupported v2", err)
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
