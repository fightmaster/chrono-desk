package finishchange

import (
	"sync"
	"testing"
	"time"
)

func TestSignalFullWakeDoesNotBlockWriterAndCloseIsConcurrentSafe(t *testing.T) {
	s := New()
	var workers sync.WaitGroup
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for n := 0; n < 10000; n++ {
				s.Notify()
			}
		}()
	}
	s.Close()
	s.Close()
	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("unconsumed wake blocked a writer")
	}
	if s.Generation() != 40000 || len(s.Wake()) != 1 {
		t.Fatalf("generation=%d queued=%d", s.Generation(), len(s.Wake()))
	}
	select {
	case <-s.Done():
	default:
		t.Fatal("close did not cancel observers")
	}
}
