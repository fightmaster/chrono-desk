package service

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

type SyncPullResult struct {
	Changes ChangePullStats `json:"changes"`
	Recount *RecountStats   `json:"recount,omitempty"`
}

// SyncPullManager owns both manual and live-session pulls. A per-event mutex
// prevents overlapping feed reads and recounts from advancing the same cursor
// or rebuilding the same projection concurrently.
type SyncPullManager struct {
	events   *EventService
	logger   *log.Logger
	interval time.Duration

	mu       sync.Mutex
	sessions map[string]context.CancelFunc
	locks    map[string]*sync.Mutex
}

func NewSyncPullManager(events *EventService, logger *log.Logger, interval time.Duration) *SyncPullManager {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &SyncPullManager{
		events: events, logger: logger, interval: interval,
		sessions: map[string]context.CancelFunc{}, locks: map[string]*sync.Mutex{},
	}
}

func (m *SyncPullManager) Start(eventID string) {
	m.mu.Lock()
	if _, running := m.sessions[eventID]; running {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.sessions[eventID] = cancel
	m.mu.Unlock()

	go func() {
		m.pullAndLog(ctx, eventID)
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.pullAndLog(ctx, eventID)
			}
		}
	}()
}

func (m *SyncPullManager) Stop(eventID string) {
	m.mu.Lock()
	cancel := m.sessions[eventID]
	delete(m.sessions, eventID)
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (m *SyncPullManager) StopAll() {
	m.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(m.sessions))
	for eventID, cancel := range m.sessions {
		cancels = append(cancels, cancel)
		delete(m.sessions, eventID)
	}
	m.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (m *SyncPullManager) Running(eventID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, running := m.sessions[eventID]
	return running
}

func (m *SyncPullManager) PullNow(ctx context.Context, eventID string) (SyncPullResult, error) {
	lock := m.eventLock(eventID)
	lock.Lock()
	defer lock.Unlock()

	store, err := m.events.Open(eventID)
	if err != nil {
		return SyncPullResult{}, err
	}
	config, err := store.GetSyncConfig(ctx, eventID)
	if err != nil {
		return SyncPullResult{}, err
	}
	if config.BaseURL == "" || config.Token == "" {
		return SyncPullResult{}, fmt.Errorf("настройте адрес сайта и токен синхронизации")
	}
	changes, err := PullEventChanges(ctx, store, config.BaseURL, config.Token, eventID, time.Now())
	if err != nil {
		return SyncPullResult{}, err
	}
	result := SyncPullResult{Changes: changes}
	if changes.Observations > 0 {
		recount, err := NewRecounter(store, m.logger, false).Recount(ctx, eventID, "")
		if err != nil {
			return SyncPullResult{}, err
		}
		result.Recount = &recount
	}
	return result, nil
}

func (m *SyncPullManager) eventLock(eventID string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if lock := m.locks[eventID]; lock != nil {
		return lock
	}
	lock := &sync.Mutex{}
	m.locks[eventID] = lock
	return lock
}

func (m *SyncPullManager) pullAndLog(ctx context.Context, eventID string) {
	if _, err := m.PullNow(ctx, eventID); err != nil && ctx.Err() == nil {
		m.logger.Printf("background sync pull %s: %v", eventID, err)
	}
}
