package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckpointWindowExportRoundTripAndSyncCapability(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	importFixture(t, store)
	for _, edit := range []EditRequest{
		{Entity: "checkpoint", EntityID: "cp-finish", Field: "until_offset_seconds", Value: json.RawMessage(`3600`)},
		{Entity: "checkpoint", EntityID: "cp-finish", Field: "read_selection", Value: json.RawMessage(`"last"`)},
	} {
		if _, err := ApplyEdit(ctx, store, edit); err != nil {
			t.Fatal(err)
		}
	}
	data, _, err := BuildEventExport(ctx, store, "ev-100")
	if err != nil {
		t.Fatal(err)
	}
	document, err := ParseEventExport(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if document.SchemaVersion != 4 {
		t.Fatal("extended event exported as legacy")
	}
	target := newTestStore(t)
	if _, err := NewEventImporter(target).Import(ctx, document); err != nil {
		t.Fatal(err)
	}
	cp, err := target.GetCheckpoint(ctx, "cp-finish")
	if err != nil {
		t.Fatal(err)
	}
	if cp.ReadSelection != "last" || cp.UntilOffsetSeconds == nil || *cp.UntilOffsetSeconds != 3600 {
		t.Fatal("window lost in export/import")
	}
	payload, _, err := BuildSyncPayload(ctx, store, "ev-100", false)
	if err != nil {
		t.Fatal(err)
	}
	var request map[string]any
	if err := json.Unmarshal(payload, &request); err != nil {
		t.Fatal(err)
	}
	if request["checkpoint_window_selection_version"] != float64(1) {
		t.Fatal("extended sync omitted capability requirement")
	}
	document.SchemaVersion = 3
	invalid, _ := json.Marshal(document)
	if _, err := ParseEventExport(bytes.NewReader(invalid)); err == nil {
		t.Fatal("extended settings accepted under legacy schema")
	}
}

func TestWindowSyncRefusesOldServerBeforePosting(t *testing.T) {
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"push_schema_versions":[2,3]}`))
	}))
	defer server.Close()
	_, err := PushSync(context.Background(), server.URL, "synthetic", "ev-100", []byte(`{"schema_version":3,"checkpoint_window_selection_version":1}`))
	if err == nil || !strings.Contains(err.Error(), "окна чекпоинта") || posts != 0 {
		t.Fatalf("unsafe old-server sync err=%v posts=%d", err, posts)
	}
}
