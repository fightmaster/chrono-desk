package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

func TestPullEventChangesAppliesAllPagesAndResumesFromCommittedCursor(t *testing.T) {
	store := newPullStore(t)
	var requestedAfter []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/sync/events/ev1/capabilities":
			_, _ = w.Write([]byte(`{"change_feed_schema_versions":[1],"preferred_change_feed_schema_version":1}`))
		case r.URL.Path == "/api/sync/events/ev1/changes":
			after := r.URL.Query().Get("after")
			requestedAfter = append(requestedAfter, after)
			if after == "" {
				_, _ = w.Write([]byte(feedPage("cursor-1", true, "remote-1", 1000)))
				return
			}
			if after == "cursor-1" {
				_, _ = w.Write([]byte(feedPage("cursor-2", false, "remote-2", 2000)))
				return
			}
			_, _ = w.Write([]byte(`{"schema_version":1,"items":[],"next_cursor":"cursor-2","has_more":false}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	stats, err := PullEventChanges(context.Background(), store, srv.URL, "token", "ev1", time.UnixMilli(3000))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pages != 2 || stats.Observations != 2 {
		t.Fatalf("stats = %+v", stats)
	}
	if fmt.Sprint(requestedAfter) != "[ cursor-1]" {
		t.Fatalf("after sequence = %v", requestedAfter)
	}

	requestedAfter = nil
	stats, err = PullEventChanges(context.Background(), store, srv.URL, "token", "ev1", time.UnixMilli(4000))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Observations != 0 || fmt.Sprint(requestedAfter) != "[cursor-2]" {
		t.Fatalf("resume stats=%+v after=%v", stats, requestedAfter)
	}
}

func TestPullEventChangesFailsClosedWithoutFeedV1(t *testing.T) {
	store := newPullStore(t)
	var feedRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sync/events/ev1/capabilities" {
			feedRequests++
		}
		_, _ = w.Write([]byte(`{"change_feed_schema_versions":[]}`))
	}))
	defer srv.Close()

	if _, err := PullEventChanges(context.Background(), store, srv.URL, "token", "ev1", time.Now()); err == nil {
		t.Fatal("expected unsupported feed error")
	}
	if feedRequests != 0 {
		t.Fatalf("feed requests = %d", feedRequests)
	}
}

func TestPullEventChangesKeepsFirstPageCursorWhenSecondPageIsInvalid(t *testing.T) {
	store := newPullStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sync/events/ev1/capabilities" {
			_, _ = w.Write([]byte(`{"change_feed_schema_versions":[1]}`))
			return
		}
		if r.URL.Query().Get("after") == "" {
			_, _ = w.Write([]byte(feedPage("cursor-1", true, "remote-1", 1000)))
			return
		}
		_, _ = w.Write([]byte(feedPage("cursor-2", false, "remote-1", 9999)))
	}))
	defer srv.Close()

	if _, err := PullEventChanges(context.Background(), store, srv.URL, "token", "ev1", time.Now()); err == nil {
		t.Fatal("expected immutable conflict on second page")
	}
	cursor, err := store.GetPullCursor(context.Background(), "ev1")
	if err != nil || cursor == nil || *cursor != "cursor-1" {
		t.Fatalf("cursor = %v, err = %v", cursor, err)
	}
}

func newPullStore(t *testing.T) *sqlite.Store {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "event.chrono"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := sqlite.New(db, sqlite.WithOriginInstanceID("desk-1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertEvent(context.Background(), domain.Event{ID: "ev1", Name: "Event"}); err != nil {
		t.Fatal(err)
	}
	return store
}

func feedPage(cursor string, more bool, id string, timeMs int64) string {
	return fmt.Sprintf(`{"schema_version":1,"items":[{"type":"observation_created","observation":{"id":%q,"event_id":"ev1","observation_version":1,"capture_source_id":"stopwatch:start","origin_system":"stopwatch","origin_instance_id":"watch-1","origin_sequence":1,"status":0,"number":42,"time_ms":%d,"ant":1,"epc":"","rssi":-50,"board":"start","disabled_at":null}}],"next_cursor":%q,"has_more":%t}`,
		id, timeMs, cursor, more)
}
