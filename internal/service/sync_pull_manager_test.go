package service

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

func TestSyncPullManagerFetchesWhileRunningAndStops(t *testing.T) {
	var feedRequests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sync/events/ev1/capabilities" {
			_, _ = w.Write([]byte(`{"change_feed_schema_versions":[1]}`))
			return
		}
		request := feedRequests.Add(1)
		if request == 1 {
			_, _ = w.Write([]byte(feedPage("cursor-live", false, "remote-live", 1000)))
			return
		}
		_, _ = w.Write([]byte(`{"schema_version":1,"items":[],"next_cursor":"cursor-live","has_more":false}`))
	}))
	defer srv.Close()

	events, store := newPullManagerEvent(t, srv.URL)
	manager := NewSyncPullManager(events, log.New(io.Discard, "", 0), 15*time.Millisecond)
	manager.Start("ev1")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if count, _ := store.CountRfidLogs(context.Background(), "ev1"); count == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	manager.Stop("ev1")
	if count, err := store.CountRfidLogs(context.Background(), "ev1"); err != nil || count != 1 {
		t.Fatalf("background imported count=%d err=%v", count, err)
	}
	if manager.Running("ev1") {
		t.Fatal("manager is still running after stop")
	}
	var outbox int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM observation_outbox`).Scan(&outbox); err != nil || outbox != 0 {
		t.Fatalf("remote observation became owned: outbox=%d err=%v", outbox, err)
	}
}

func TestSyncPullManagerSerializesManualAndBackgroundPullsPerEvent(t *testing.T) {
	var active, maximum atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sync/events/ev1/capabilities" {
			_, _ = w.Write([]byte(`{"change_feed_schema_versions":[1]}`))
			return
		}
		current := active.Add(1)
		for {
			old := maximum.Load()
			if current <= old || maximum.CompareAndSwap(old, current) {
				break
			}
		}
		defer active.Add(-1)
		time.Sleep(30 * time.Millisecond)
		_, _ = w.Write([]byte(`{"schema_version":1,"items":[],"next_cursor":"cursor-empty","has_more":false}`))
	}))
	defer srv.Close()

	events, _ := newPullManagerEvent(t, srv.URL)
	manager := NewSyncPullManager(events, log.New(io.Discard, "", 0), time.Hour)
	var wg sync.WaitGroup
	wg.Add(2)
	for range 2 {
		go func() {
			defer wg.Done()
			if _, err := manager.PullNow(context.Background(), "ev1"); err != nil {
				t.Errorf("pull now: %v", err)
			}
		}()
	}
	wg.Wait()
	if maximum.Load() != 1 {
		t.Fatalf("concurrent feed requests = %d, want 1", maximum.Load())
	}
}

func newPullManagerEvent(t *testing.T, baseURL string) (*EventService, *sqlite.Store) {
	t.Helper()
	dir := t.TempDir()
	db, err := sqlite.Open(filepath.Join(dir, "ev1.chrono"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.New(db, sqlite.WithOriginInstanceID("desk-1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertEvent(context.Background(), domain.Event{ID: "ev1", Name: "Event"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSyncConfig(context.Background(), "ev1", baseURL, "token"); err != nil {
		t.Fatal(err)
	}
	events, err := NewEventManager(dir, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(events.Close)
	return events, store
}
