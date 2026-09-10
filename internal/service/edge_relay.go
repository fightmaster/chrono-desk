package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/rfid-core/edge"
	"gitlab.com/fightmaster1/rfid-core/tcp"
)

// EdgeRelayManager owns a bounded set of independent event senders. Their
// settings persist; no input listener, site-pull cursor or browser must remain
// active for a configured queue to drain after an application restart.
type EdgeRelayManager struct {
	events   *EventService
	logger   *log.Logger
	interval time.Duration
	mu       sync.Mutex
	closed   bool
	sessions map[string]*edgeRelaySession
}

type edgeRelaySession struct {
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	error  string
}

func NewEdgeRelayManager(events *EventService, logger *log.Logger, interval time.Duration) *EdgeRelayManager {
	if interval <= 0 {
		interval = time.Second
	}
	return &EdgeRelayManager{events: events, logger: logger, interval: interval, sessions: map[string]*edgeRelaySession{}}
}

func (m *EdgeRelayManager) ResumeConfigured(ctx context.Context) error {
	events, err := m.events.List(ctx)
	if err != nil {
		return err
	}
	for _, event := range events {
		if err := m.Resume(event.ID); err != nil {
			m.logger.Printf("edge relay %s could not resume: %v", event.ID, err)
		}
	}
	return nil
}

func (m *EdgeRelayManager) Resume(eventID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("досылка приложения уже остановлена")
	}
	store, err := m.events.Open(eventID)
	if err != nil {
		return err
	}
	config, err := store.EdgeRelayConfig(context.Background(), eventID)
	if err != nil {
		return err
	}
	return m.startLocked(store, eventID, config)
}

func (m *EdgeRelayManager) Configure(ctx context.Context, eventID string, config domain.EdgeRelayConfig, confirmPending bool) (domain.EdgeRelayConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return domain.EdgeRelayConfig{}, errors.New("досылка приложения уже остановлена")
	}
	store, err := m.events.Open(eventID)
	if err != nil {
		return domain.EdgeRelayConfig{}, err
	}
	if config.Enabled && !m.runningLocked(eventID) && m.runningCountLocked() >= 16 {
		return domain.EdgeRelayConfig{}, errors.New("одновременно можно досылать не более 16 событий")
	}
	// Join this process's in-flight ACK before changing the saved destination.
	// Other processes are fenced by storage revision/claim tokens.
	if session := m.sessions[eventID]; session != nil {
		session.cancel()
		<-session.done
	}
	saved, err := store.ConfigureEdgeRelay(ctx, eventID, config, confirmPending)
	if err != nil {
		// A failed/stale UI command must not leave an enabled queue stopped.
		previous, readErr := store.EdgeRelayConfig(context.Background(), eventID)
		if readErr == nil {
			_ = m.startLocked(store, eventID, previous)
		}
		return domain.EdgeRelayConfig{}, err
	}
	return saved, m.startLocked(store, eventID, saved)
}

func (m *EdgeRelayManager) startLocked(store *sqlite.Store, eventID string, config domain.EdgeRelayConfig) error {
	if !config.Enabled || m.runningLocked(eventID) {
		return nil
	}
	if m.runningCountLocked() >= 16 {
		return errors.New("лимит 16 активных событий; очередь сохранена")
	}
	ctx, cancel := context.WithCancel(context.Background())
	session := &edgeRelaySession{cancel: cancel, done: make(chan struct{})}
	m.sessions[eventID] = session
	go session.run(ctx, store, eventID, config, m.interval)
	return nil
}

func (m *EdgeRelayManager) runningLocked(eventID string) bool {
	if session := m.sessions[eventID]; session != nil {
		select {
		case <-session.done:
		default:
			return true
		}
	}
	return false
}

func (m *EdgeRelayManager) runningCountLocked() int {
	count := 0
	for eventID := range m.sessions {
		if m.runningLocked(eventID) {
			count++
		}
	}
	return count
}

func (m *EdgeRelayManager) Status(eventID string) (running bool, lastError string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if session := m.sessions[eventID]; session != nil {
		session.mu.Lock()
		defer session.mu.Unlock()
		return m.runningLocked(eventID), session.error
	}
	return false, ""
}

func (m *EdgeRelayManager) StopAll() {
	m.mu.Lock()
	m.closed = true
	sessions := make([]*edgeRelaySession, 0, len(m.sessions))
	for _, session := range m.sessions {
		session.cancel()
		sessions = append(sessions, session)
	}
	m.mu.Unlock()
	for _, session := range sessions {
		<-session.done
	}
}

func (s *edgeRelaySession) setError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.error = ""
	if err != nil {
		s.error = err.Error()
	}
}

func (s *edgeRelaySession) run(ctx context.Context, store *sqlite.Store, eventID string, config domain.EdgeRelayConfig, interval time.Duration) {
	defer close(s.done)
	client := &tcp.LineClient{Endpoint: config.Endpoint, Timeout: 2 * time.Second}
	defer client.Close()
	for ctx.Err() == nil {
		claim, found, err := store.ClaimEdgeRelay(ctx, eventID, config, time.Now())
		if err != nil || !found {
			s.setError(err)
			if err == nil {
				current, readErr := store.EdgeRelayConfig(ctx, eventID)
				if readErr == nil && current != config {
					return
				}
			}
			if !waitRelay(ctx, interval) {
				return
			}
			continue
		}
		packet, deliveryErr := edge.Decode(claim.Payload)
		if deliveryErr == nil && (packet.ID != claim.ObservationID || strconv.FormatInt(packet.ExternalEventID, 10) != eventID) {
			deliveryErr = fmt.Errorf("сохранённый edge-пакет не соответствует журналу события")
		}
		if deliveryErr == nil {
			deliveryErr = client.Send(ctx, claim.Payload, strings.TrimSuffix(string(edge.ACK(packet)), "\n"))
		}
		if deliveryErr != nil {
			_ = client.Close()
		}
		// Cleanup may record an already received ACK or release a cancelled
		// attempt even when shutdown cancelled the network operation.
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		now := time.Now()
		err = store.FinishEdgeRelay(finishCtx, eventID, claim, now, deliveryErr, now.Add(edgeRelayBackoff(claim.Attempts)))
		cancel()
		if err != nil {
			s.setError(err)
		} else {
			s.setError(deliveryErr)
		}
	}
}

func edgeRelayBackoff(attempts int64) time.Duration {
	base := min(30*time.Second, time.Second<<min(max(attempts-1, 0), 5))
	return base*3/4 + rand.N(base/4)
}

func waitRelay(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
