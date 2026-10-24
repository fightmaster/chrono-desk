package publicweb

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

// A real socket can buffer a ten-row frame and hide a stalled peer. This writer
// deterministically blocks after the initial frame and honours write deadlines.
type blockedFinishWriter struct {
	header   http.Header
	writes   atomic.Int64
	deadline atomic.Int64
	initial  chan struct{}
	blocked  chan struct{}
	release  chan struct{}
	once     sync.Once
}

func TestFinishStreamRealSocketDeadlineReleasesNonreadingPeer(t *testing.T) {
	s := newServer(t)
	s.publishedID = "ev-100"
	ts := httptest.NewUnstartedServer(s.routes())
	ts.Config.ConnContext = func(ctx context.Context, conn net.Conn) context.Context {
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetWriteBuffer(1024)
		}
		return ctx
	}
	ts.Start()
	defer ts.Close()
	fast := openLoadFinishClient(t, ts.URL+"/api/live/finishes/stream", true)
	defer fast.close()
	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetReadBuffer(1024)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, err = fmt.Fprintf(conn, "GET /api/live/finishes/stream HTTP/1.1\r\nHost: %s\r\n\r\n", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(response.Body)
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
		t.Fatal("slow peer initial snapshot missing")
	}
	// Stop reading after the small initial frame. Constrain both socket buffers
	// and send a larger legitimate frame so kernel buffering cannot mask a stall.
	store, _ := s.events.Open("ev-100")
	if err := store.UpsertEvent(context.Background(), domain.Event{ID: "ev-100", Name: strings.Repeat("x", 64*1024)}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 16; i++ {
		commitTestFinish(t, s, i)
	}
	s.mu.Lock()
	h := s.finishHub
	s.mu.Unlock()
	waitFinishClients(t, h, 1)
	deadline := time.Now().Add(time.Second)
	for fast.lastNumber.Load() != 16 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if fast.lastNumber.Load() != 16 {
		t.Fatal("normal peer stopped receiving while stalled socket expired")
	}
}

func (w *blockedFinishWriter) Header() http.Header { return w.header }
func (w *blockedFinishWriter) WriteHeader(int)     {}
func (w *blockedFinishWriter) FlushError() error   { return nil }
func (w *blockedFinishWriter) SetWriteDeadline(t time.Time) error {
	w.deadline.Store(t.UnixNano())
	return nil
}
func (w *blockedFinishWriter) Write(p []byte) (int, error) {
	if w.writes.Add(1) == 1 {
		close(w.initial)
		return len(p), nil
	}
	w.once.Do(func() { close(w.blocked) })
	timer := time.NewTimer(time.Until(time.Unix(0, w.deadline.Load())))
	defer timer.Stop()
	select {
	case <-w.release:
		return len(p), nil
	case <-timer.C:
		return 0, os.ErrDeadlineExceeded
	}
}

func TestFinishStreamBlockedWriterDoesNotBlockCommittedUpdates(t *testing.T) {
	s := newServer(t)
	s.publishedID = "ev-100"
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	a := openTestFinishStream(t, ts.URL+"/api/live/finishes/stream")
	defer a.close()
	a.next(t)
	b := openTestFinishStream(t, ts.URL+"/api/live/finishes/stream")
	defer b.close()
	b.next(t)
	w := &blockedFinishWriter{header: make(http.Header), initial: make(chan struct{}), blocked: make(chan struct{}), release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.handleFinishStream(w, httptest.NewRequest(http.MethodGet, "/api/live/finishes/stream", nil).WithContext(ctx))
	}()
	defer func() {
		cancel()
		close(w.release)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("blocked handler leaked")
		}
	}()
	select {
	case <-w.initial:
	case <-time.After(time.Second):
		t.Fatal("no initial frame")
	}
	commitTestFinish(t, s, 1)
	select {
	case <-w.blocked:
	case <-time.After(time.Second):
		t.Fatal("slow writer did not block")
	}
	for _, client := range []*testFinishStream{a, b} {
		_, data := client.next(t)
		if *data.Rows[0].Number != 1 {
			t.Fatal(data)
		}
	}
	committed := make(chan struct{})
	go func() { defer close(committed); commitTestFinish(t, s, 2) }()
	select {
	case <-committed:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("writer blocked the producer commit")
	}
	for _, client := range []*testFinishStream{a, b} {
		_, data := client.next(t)
		if *data.Rows[0].Number != 2 {
			t.Fatal(data)
		}
	}
	// The deadline, not a cooperative peer, must release the handler.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("write deadline did not stop the slow client")
	}
}
