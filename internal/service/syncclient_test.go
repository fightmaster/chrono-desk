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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-SYNC-TOKEN")
		gotPath = r.URL.Path
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
	if resp["rfid_inserted"].(float64) != 5 {
		t.Errorf("summary = %+v", resp)
	}
}

func TestPushSyncMapsErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"токен не подходит к событию"}`))
	}))
	defer srv.Close()

	_, err := PushSync(context.Background(), srv.URL, "bad", "936919", []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "токен не подходит") {
		t.Fatalf("err = %v, want forbidden message", err)
	}
}

func TestSyncURLRequiresBase(t *testing.T) {
	if _, err := syncURL("  ", "936919"); err == nil {
		t.Error("empty base url must error")
	}
}
