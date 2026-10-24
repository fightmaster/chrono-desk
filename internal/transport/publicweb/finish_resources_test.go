package publicweb

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"testing"
	"time"
)

func waitFinishClients(t *testing.T, h *finishHub, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		clients, groups := h.clients, len(h.groups)
		h.mu.Unlock()
		if clients == want && (want != 0 || (groups == 0 && h.activeWorkers.Load() == 0)) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	t.Fatalf("clients=%d groups=%d workers=%d; want clients=%d", h.clients, len(h.groups), h.activeWorkers.Load(), want)
}

func finishResourceProfile(t *testing.T, dir, name string) runtime.MemStats {
	t.Helper()
	runtime.GC()
	if dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for _, kind := range []string{"heap", "goroutine"} {
			file, err := os.Create(filepath.Join(dir, name+"-"+kind+".pprof"))
			if err != nil {
				t.Fatal(err)
			}
			if err := pprof.Lookup(kind).WriteTo(file, 0); err != nil {
				file.Close()
				t.Fatal(err)
			}
			file.Close()
		}
	}
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats
}

func TestFinishStreamThousandHTTPReconnectsReleaseResources(t *testing.T) {
	s := newServer(t)
	s.publishedID = "ev-100"
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	transport := &http.Transport{DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	var h *finishHub
	cycle := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/live/finishes/stream", nil)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != 200 {
			res.Body.Close()
			t.Fatalf("status=%d", res.StatusCode)
		}
		scanner := bufio.NewScanner(res.Body)
		found := false
		for scanner.Scan() {
			if scanner.Text() == "event: snapshot" {
				found = true
			}
			if found && scanner.Text() == "" {
				break
			}
		}
		if !found {
			t.Fatal("initial snapshot missing")
		}
		cancel()
		res.Body.Close()
		if h == nil {
			s.mu.Lock()
			h = s.finishHub
			s.mu.Unlock()
		}
		waitFinishClients(t, h, 0)
	}
	for i := 0; i < 25; i++ {
		cycle()
	}
	profileDir := os.Getenv("CHRONO_PRESENTER_PROFILE_DIR")
	warm := finishResourceProfile(t, profileDir, "warm")
	warmGoroutines := runtime.NumGoroutine()
	warmFDs, _ := os.ReadDir("/proc/self/fd")
	// Frozen before this HTTP-cycle measurement. The isolated hub baseline has
	// no retained application objects; allow 2MiB for runtime/profile caches.
	const retainedHeapBudget = 2 * 1024 * 1024
	var final runtime.MemStats
	for batch := 1; batch <= 4; batch++ {
		for i := 0; i < 250; i++ {
			cycle()
		}
		transport.CloseIdleConnections()
		final = finishResourceProfile(t, profileDir, "batch-"+string(rune('0'+batch)))
		t.Logf("cycles=%d retained_heap_delta=%d goroutines=%d", batch*250, int64(final.HeapAlloc)-int64(warm.HeapAlloc), runtime.NumGoroutine())
	}
	store, _ := s.events.Open("ev-100")
	if store.ReadStats().InUse != 0 {
		t.Fatal("cancelled clients retained DB leases")
	}
	if growth := int64(final.HeapAlloc) - int64(warm.HeapAlloc); growth > retainedHeapBudget {
		t.Fatalf("retained heap grew %d bytes (budget %d)", growth, retainedHeapBudget)
	}
	if runtime.NumGoroutine() > warmGoroutines+2 {
		t.Fatalf("goroutines grew from %d to %d", warmGoroutines, runtime.NumGoroutine())
	}
	if finalFDs, err := os.ReadDir("/proc/self/fd"); err == nil && len(warmFDs) > 0 && len(finalFDs) > len(warmFDs)+2 {
		t.Fatalf("FDs grew from %d to %d", len(warmFDs), len(finalFDs))
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.done:
	default:
		t.Fatal("dispatcher retained after shutdown")
	}
}

func TestFinishStreamAdmissionIsBoundedAndReleased(t *testing.T) {
	s := newServer(t)
	s.publishedID = "ev-100"
	h, err := s.finishHubFor(context.Background(), "ev-100")
	if err != nil {
		t.Fatal(err)
	}
	defer stopTestFinishHub(t, h)
	var subscribers []*finishSubscription
	defer func() {
		for _, sub := range subscribers {
			sub.close()
		}
	}()
	for i := 0; i < maxFinishClients; i++ {
		sub, err := h.subscribe("")
		if err != nil {
			t.Fatal(err)
		}
		subscribers = append(subscribers, sub)
	}
	if _, err := h.subscribe(""); err != errFinishStreamBusy {
		t.Fatalf("expected admission rejection, got %v", err)
	}
	subscribers[0].close()
	sub, err := h.subscribe("")
	if err != nil {
		t.Fatal(err)
	}
	sub.close()
	for _, sub := range subscribers {
		sub.close()
	}
	waitFinishClients(t, h, 0)
}
