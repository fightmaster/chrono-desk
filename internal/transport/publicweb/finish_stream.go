package publicweb

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/service"
)

func (s *Server) finishHubFor(ctx context.Context, eventID string) (*finishHub, error) {
	for {
		s.mu.Lock()
		if s.publishedID != eventID || eventID == "" {
			s.mu.Unlock()
			return nil, context.Canceled
		}
		h := s.finishHub
		if h != nil && h.eventID == eventID && h.epoch == s.publicationEpoch && h.ctx.Err() == nil {
			s.mu.Unlock()
			return h, nil
		}
		if h != nil {
			h.cancel()
			s.mu.Unlock()
			// A store signal has one dispatcher. Stop it before a replacement
			// can consume wake-ups; never wait while holding the server mutex.
			select {
			case <-h.done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			s.mu.Lock()
			if s.finishHub == h {
				s.finishHub = nil
			}
			s.mu.Unlock()
			continue
		}
		s.mu.Unlock()
		signal, err := s.events.FinishChanges(eventID)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		if s.finishHub == nil && s.publishedID == eventID {
			s.hubSequence++
			prefix := fmt.Sprintf("hub:%d:", s.hubSequence)
			h = newFinishHub(eventID, s.publicationEpoch, signal, func(ctx context.Context, race string) (publicFinishes, error) {
				return s.liveFinishPayload(ctx, eventID, race)
			}, func(value string) string { return s.opaqueKey(prefix + value) })
			s.finishHub = h
			s.mu.Unlock()
			return h, nil
		}
		s.mu.Unlock()
	}
}

func (s *Server) currentFinishHub(h *finishHub) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finishHub == h && s.publishedID == h.eventID && s.publicationEpoch == h.epoch && h.ctx.Err() == nil
}

// setPublishedLocked invalidates streaming work immediately on a publication
// change. Caller holds s.mu; cancellation never performs network/DB work.
func (s *Server) setPublishedLocked(eventID string) {
	if s.publishedID == eventID {
		return
	}
	s.publishedID = eventID
	s.publicationEpoch++
	if s.finishHub != nil {
		s.finishHub.cancel()
	}
}

func (s *Server) stopFinishHub(ctx context.Context) error {
	s.mu.Lock()
	h := s.finishHub
	if h != nil {
		h.cancel()
	}
	s.mu.Unlock()
	if h == nil {
		return nil
	}
	select {
	case <-h.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) handleFinishStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "только GET", http.StatusMethodNotAllowed)
		return
	}
	initial, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	h, err := s.finishHubFor(initial, s.published())
	var sub *finishSubscription
	if err == nil {
		sub, err = h.subscribe(r.URL.Query().Get("race_id"))
	}
	if err != nil {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "трансляция временно недоступна", http.StatusServiceUnavailable)
		return
	}
	defer sub.close()
	var frame *finishFrame
	select {
	case <-sub.wake:
		frame = sub.take()
	case <-sub.ctx.Done():
		err = sub.failure()
	case <-initial.Done():
		err = initial.Err()
	}
	if err == nil && (frame == nil || !s.currentFinishHub(h)) {
		err = context.Canceled
	}
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, service.ErrLiveFinishRace) {
			status = http.StatusBadRequest
		}
		http.Error(w, "финиши временно недоступны", status)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	write := func(data []byte) error {
		if !s.currentFinishHub(h) {
			return context.Canceled
		}
		if err := controller.SetWriteDeadline(time.Now().Add(s.streamWriteTimeout)); err != nil {
			return err
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
		return controller.Flush()
	}
	if err := write(append([]byte("retry: 1000\n\n"), frame.wire...)); err != nil {
		return
	}
	heartbeat := time.NewTicker(s.streamHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-sub.ctx.Done():
			return
		case <-sub.wake:
			if frame := sub.take(); frame != nil {
				if err := write(frame.wire); err != nil {
					return
				}
			}
		case <-heartbeat.C:
			if err := write([]byte("event: heartbeat\ndata: {}\n\n")); err != nil {
				return
			}
		}
	}
}
