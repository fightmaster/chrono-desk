package service

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

// PhotoManager polls registered Chrono Cam phones over the LAN and stores their
// finish photos for matching against timing. Read-only with respect to the timing
// path. Mirrors LiveManager's lifecycle (one poller per event; StopAll on shutdown).
type PhotoManager struct {
	logger *log.Logger
	client *http.Client

	mu      sync.Mutex
	pollers map[string]*photoPoller // eventID -> poller
}

type photoPoller struct {
	cancel context.CancelFunc
}

// PollStats summarises one poll cycle.
type PollStats struct {
	Sources int `json:"sources"`
	Photos  int `json:"photos"`
	Errors  int `json:"errors"`
}

func NewPhotoManager(logger *log.Logger) *PhotoManager {
	return &PhotoManager{
		logger:  logger,
		client:  &http.Client{Timeout: 5 * time.Second},
		pollers: make(map[string]*photoPoller),
	}
}

// Start begins background polling for an event (idempotent).
func (m *PhotoManager) Start(store *sqlite.Store, eventID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.pollers[eventID]; ok {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.pollers[eventID] = &photoPoller{cancel: cancel}
	go m.loop(ctx, store, eventID)
}

// Running reports whether a poller is active for the event.
func (m *PhotoManager) Running(eventID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.pollers[eventID]
	return ok
}

func (m *PhotoManager) Stop(eventID string) {
	m.mu.Lock()
	p := m.pollers[eventID]
	delete(m.pollers, eventID)
	m.mu.Unlock()
	if p != nil {
		p.cancel()
	}
}

func (m *PhotoManager) StopAll() {
	m.mu.Lock()
	pollers := m.pollers
	m.pollers = make(map[string]*photoPoller)
	m.mu.Unlock()
	for _, p := range pollers {
		p.cancel()
	}
}

func (m *PhotoManager) loop(ctx context.Context, store *sqlite.Store, eventID string) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		if _, err := m.PollOnce(ctx, store, eventID); err != nil && ctx.Err() == nil {
			m.logger.Printf("photo poll %s: %v", eventID, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// PollOnce pulls every enabled source for the event once and upserts its photos.
func (m *PhotoManager) PollOnce(ctx context.Context, store *sqlite.Store, eventID string) (PollStats, error) {
	sources, err := store.ListPhotoSources(ctx, eventID)
	if err != nil {
		return PollStats{}, err
	}
	var stats PollStats
	for _, src := range sources {
		if !src.Enabled {
			continue
		}
		stats.Sources++
		ev, tracks, err := pullChronoCam(ctx, m.client, src.BaseURL)
		if err != nil {
			stats.Errors++
			m.logger.Printf("photo source %s: %v", src.BaseURL, err)
			continue
		}
		skew := time.Now().UnixMilli() - ev.ServerTimeEpochMs
		src.SourceID = ev.SourceID
		src.CameraLabel = ev.CameraLabel
		src.SkewMs = skew
		src.LastSeenAt = time.Now().UnixMilli()
		if err := store.UpsertPhotoSource(ctx, eventID, src); err != nil {
			m.logger.Printf("update photo source %s: %v", src.BaseURL, err)
		}
		now := time.Now().UnixMilli()
		for _, t := range tracks {
			p := toStoredPhoto(ev, src.BaseURL, t, skew)
			if err := store.UpsertPhoto(ctx, eventID, p, now); err != nil {
				stats.Errors++
				m.logger.Printf("store photo %s: %v", p.ID, err)
				continue
			}
			stats.Photos++
		}
	}
	return stats, nil
}

const pollInterval = 3 * time.Second
