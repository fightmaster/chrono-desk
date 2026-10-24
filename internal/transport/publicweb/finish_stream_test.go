package publicweb

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

type testFinishStream struct {
	response *http.Response
	scanner  *bufio.Scanner
	cancel   context.CancelFunc
}

func openTestFinishStream(t *testing.T, url string) *testFinishStream {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Last-Event-ID", "previous-process-unknown-id")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		cancel()
		t.Fatalf("stream status %d", res.StatusCode)
	}
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatal(res.Header)
	}
	stream := &testFinishStream{response: res, scanner: bufio.NewScanner(res.Body), cancel: cancel}
	t.Cleanup(stream.close)
	return stream
}

func (s *testFinishStream) close() { s.cancel(); s.response.Body.Close() }

func (s *testFinishStream) next(t *testing.T) (string, publicFinishes) {
	t.Helper()
	var event, id string
	var data publicFinishes
	for s.scanner.Scan() {
		line := s.scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "id: "):
			id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "data: ") && event == "snapshot":
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &data); err != nil {
				t.Fatal(err)
			}
		case line == "" && event == "snapshot":
			return id, data
		}
	}
	t.Fatalf("stream ended: %v", s.scanner.Err())
	return "", data
}

func commitTestFinish(t *testing.T, s *Server, n int) {
	t.Helper()
	store, err := s.events.Open("ev-100")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	clean := "00:20:00"
	finish := int64(1800000000000 + n)
	number := int64(n)
	if err := store.UpsertMember(ctx, domain.Member{ID: fmt.Sprintf("sse-%d", n), EventID: "ev-100", RaceID: "race-10k", FirstName: "Synthetic", LastName: "Runner", Number: &number, FinishTimeMs: &finish, CleanTime: &clean}); err != nil {
		t.Fatal(err)
	}
}

func TestFinishStreamTwoClientsUpdatesReconnectAndBound(t *testing.T) {
	s := newServer(t)
	s.publishedID = "ev-100"
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	a := openTestFinishStream(t, ts.URL+"/api/live/finishes/stream")
	b := openTestFinishStream(t, ts.URL+"/api/live/finishes/stream")
	defer a.close()
	defer b.close()
	idA, initialA := a.next(t)
	idB, initialB := b.next(t)
	if idA != idB || !initialA.Published || !initialB.Published {
		t.Fatal("initial snapshot mismatch")
	}
	for n := 1; n <= 3; n++ {
		commitTestFinish(t, s, n)
		for _, client := range []*testFinishStream{a, b} {
			id, data := client.next(t)
			if id == idA || len(data.Rows) == 0 || *data.Rows[0].Number != int64(n) {
				t.Fatalf("missing update %d: %+v", n, data)
			}
		}
	}
	a.close()
	commitTestFinish(t, s, 4)
	_, data := b.next(t)
	if *data.Rows[0].Number != 4 {
		t.Fatal("closing A blocked B")
	}
	for n := 5; n <= 34; n++ {
		commitTestFinish(t, s, n)
	}
	c := openTestFinishStream(t, ts.URL+"/api/live/finishes/stream")
	defer c.close()
	_, data = c.next(t)
	if len(data.Rows) != 10 || *data.Rows[0].Number != 34 || *data.Rows[9].Number != 25 {
		t.Fatalf("latest ten incorrect: %+v", data.Rows)
	}
	keys := map[string]bool{}
	for _, row := range data.Rows {
		if keys[row.Key] {
			t.Fatal("duplicate participation")
		}
		keys[row.Key] = true
	}
}

func TestFinishStreamIdleHeartbeatDoesNotReadSnapshots(t *testing.T) {
	s := newServer(t)
	s.publishedID = "ev-100"
	s.streamHeartbeat = 10 * time.Millisecond
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	a := openTestFinishStream(t, ts.URL+"/api/live/finishes/stream")
	defer a.close()
	a.next(t)
	s.mu.Lock()
	h := s.finishHub
	s.mu.Unlock()
	before := h.builds.Load()
	for heartbeats := 0; heartbeats < 3 && a.scanner.Scan(); {
		if a.scanner.Text() == "event: heartbeat" {
			heartbeats++
		}
	}
	if h.builds.Load() != before {
		t.Fatal("heartbeat queried the database")
	}
	store, _ := s.events.Open("ev-100")
	if store.ReadStats().InUse != 0 {
		t.Fatal("idle stream pinned a DB lease")
	}
}

func TestFinishStreamInvalidFilterAndMethod(t *testing.T) {
	s := newServer(t)
	s.publishedID = "ev-100"
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	for _, item := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/api/live/finishes/stream?race_id=missing", 400},
		{http.MethodPost, "/api/live/finishes/stream", 405},
		{http.MethodHead, "/api/live/finishes/stream", 405},
	} {
		req, _ := http.NewRequest(item.method, ts.URL+item.path, nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != item.status {
			t.Fatalf("%s %s: %d", item.method, item.path, res.StatusCode)
		}
	}
}

func TestFinishStreamShutdownCancelsActiveClients(t *testing.T) {
	s := newServer(t)
	s.publishedID = "ev-100"
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	a := openTestFinishStream(t, ts.URL+"/api/live/finishes/stream")
	defer a.close()
	a.next(t)
	s.mu.Lock()
	h := s.finishHub
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	for a.scanner.Scan() {
	}
	select {
	case <-h.done:
	default:
		t.Fatal("hub dispatcher still active")
	}
	if h.activeWorkers.Load() != 0 {
		t.Fatal("view worker still active")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients != 0 || len(h.groups) != 0 {
		t.Fatalf("retained clients=%d groups=%d", h.clients, len(h.groups))
	}
}

func TestFinishStreamStoreCloseCancelsActiveClients(t *testing.T) {
	s := newServer(t)
	s.publishedID = "ev-100"
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	a := openTestFinishStream(t, ts.URL+"/api/live/finishes/stream")
	defer a.close()
	a.next(t)
	s.mu.Lock()
	h := s.finishHub
	s.mu.Unlock()
	store, err := s.events.Open("ev-100")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	for a.scanner.Scan() {
	}
	waitFinishClients(t, h, 0)
	select {
	case <-h.done:
	case <-time.After(time.Second):
		t.Fatal("closed store retained dispatcher")
	}
	if store.ReadStats().OpenConnections != 0 {
		t.Fatal("closed store retained reader connections")
	}
}
