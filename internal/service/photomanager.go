package service

import (
	"context"
	"log"
	"net/http"
	"strings"
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
	cursors map[string]int64        // eventID+source -> max first_seen ingested (incremental high-water)
	offsets map[string]float64      // eventID+source -> smoothed phone→desk clock offset (ms)
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

// pollResult carries one source's fetched data from the parallel network phase to
// the sequential DB-write phase.
type pollResult struct {
	src         sqlite.PhotoSource
	ev          chronoCamEvent
	tracks      []chronoCamTrack
	clockOffset int64
	sinceMs     int64
	err         error
}

func NewPhotoManager(logger *log.Logger) *PhotoManager {
	return &PhotoManager{
		logger:  logger,
		client:  &http.Client{Timeout: 5 * time.Second},
		pollers: make(map[string]*photoPoller),
		cursors: make(map[string]int64),
		offsets: make(map[string]float64),
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
	m.clearEventLocked(eventID)
	m.mu.Unlock()
	if p != nil {
		p.cancel()
	}
}

func (m *PhotoManager) StopAll() {
	m.mu.Lock()
	pollers := m.pollers
	m.pollers = make(map[string]*photoPoller)
	m.cursors = make(map[string]int64)
	m.offsets = make(map[string]float64)
	m.mu.Unlock()
	for _, p := range pollers {
		p.cancel()
	}
}

func (m *PhotoManager) loop(ctx context.Context, store *sqlite.Store, eventID string) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	cycle := 0
	for {
		// Mostly incremental, but every ~fullRefreshEvery cycles re-pull everything so
		// late edits (a bib assigned to an old track) still reach the desk. Cycle 0 is
		// full so the first pull after Start seeds every cursor.
		full := cycle%fullRefreshEvery == 0
		if _, err := m.pollOnce(ctx, store, eventID, full); err != nil && ctx.Err() == nil {
			m.logger.Printf("photo poll %s: %v", eventID, err)
		}
		cycle++
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// PollOnce pulls every enabled source once, in full (ignoring the incremental
// cursor). Used for explicit/manual syncs where the operator wants everything now.
func (m *PhotoManager) PollOnce(ctx context.Context, store *sqlite.Store, eventID string) (PollStats, error) {
	return m.pollOnce(ctx, store, eventID, true)
}

func (m *PhotoManager) pollOnce(ctx context.Context, store *sqlite.Store, eventID string, full bool) (PollStats, error) {
	sources, err := store.ListPhotoSources(ctx, eventID)
	if err != nil {
		return PollStats{}, err
	}

	// Fetch every source in PARALLEL: one slow/unreachable phone must not hold up the
	// healthy ones (the client has a 5s timeout, so serial polling would stall a whole
	// cycle behind a dead camera). Network only here — DB writes happen sequentially
	// below, since SQLite is happiest without concurrent writers.
	results := make([]pollResult, len(sources))
	var wg sync.WaitGroup
	for i := range sources {
		src := sources[i]
		if !src.Enabled {
			results[i] = pollResult{src: src}
			continue
		}
		sinceMs := int64(0)
		if !full {
			sinceMs = m.getCursor(eventID, src.BaseURL)
		}
		wg.Add(1)
		go func(i int, src sqlite.PhotoSource, sinceMs int64) {
			defer wg.Done()
			ev, tracks, off, err := pullChronoCam(ctx, m.client, src.BaseURL, sinceMs)
			results[i] = pollResult{src: src, ev: ev, tracks: tracks, clockOffset: off, sinceMs: sinceMs, err: err}
		}(i, src, sinceMs)
	}
	wg.Wait()

	var stats PollStats
	now := time.Now().UnixMilli()
	for _, r := range results {
		if !r.src.Enabled {
			continue
		}
		stats.Sources++
		if r.err != nil {
			stats.Errors++
			m.logger.Printf("photo source %s: %v", r.src.BaseURL, r.err)
			continue // leave the cursor untouched so we retry the same window next time
		}

		// Put every camera on this desk's clock (the shared reference), so their finish
		// times land on one timeline — essential for merging the same crossing seen by
		// two phones. The measured offset is smoothed (EMA) to shrug off per-request
		// network jitter; the manual src.SkewMs is added on top as a calibration
		// override. NOTE: this trusts the desk clock as the reference — if the desk
		// laptop's clock is wrong, correct with src.SkewMs (or disable the source).
		autoOffset := m.smoothOffset(eventID, r.src.BaseURL, r.clockOffset)
		offset := r.src.SkewMs + autoOffset
		if full {
			m.logger.Printf("photo source %s: clock offset %+d ms auto + %+d ms manual", r.src.BaseURL, autoOffset, r.src.SkewMs)
		}

		src := r.src
		src.SourceID = r.ev.SourceID
		src.CameraLabel = r.ev.CameraLabel
		src.LastSeenAt = now
		if err := store.UpsertPhotoSource(ctx, eventID, src); err != nil {
			m.logger.Printf("update photo source %s: %v", src.BaseURL, err)
		}

		maxSeen := r.sinceMs
		for _, t := range r.tracks {
			p := toStoredPhoto(r.ev, src.BaseURL, t, offset)
			if err := store.UpsertPhoto(ctx, eventID, p, now); err != nil {
				stats.Errors++
				m.logger.Printf("store photo %s: %v", p.ID, err)
				continue
			}
			stats.Photos++
			if t.FirstSeenEpochMs > maxSeen {
				maxSeen = t.FirstSeenEpochMs
			}
		}
		m.setCursor(eventID, r.src.BaseURL, maxSeen)
	}
	return stats, nil
}

// ─── per-source incremental cursor + smoothed clock offset (guarded by mu) ──────

func cursorKey(eventID, baseURL string) string { return eventID + "\x00" + baseURL }

func (m *PhotoManager) getCursor(eventID, baseURL string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cursors[cursorKey(eventID, baseURL)]
}

func (m *PhotoManager) setCursor(eventID, baseURL string, v int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cursors[cursorKey(eventID, baseURL)] = v
}

// smoothOffset folds a freshly measured phone→desk offset into a per-source EMA and
// returns the smoothed value (ms), so a single noisy round-trip can't jerk times.
func (m *PhotoManager) smoothOffset(eventID, baseURL string, measured int64) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := cursorKey(eventID, baseURL)
	next := float64(measured)
	if prev, ok := m.offsets[key]; ok {
		next = prev + offsetEMAAlpha*(float64(measured)-prev)
	}
	m.offsets[key] = next
	return int64(next)
}

func (m *PhotoManager) clearEventLocked(eventID string) {
	prefix := eventID + "\x00"
	for k := range m.cursors {
		if strings.HasPrefix(k, prefix) {
			delete(m.cursors, k)
		}
	}
	for k := range m.offsets {
		if strings.HasPrefix(k, prefix) {
			delete(m.offsets, k)
		}
	}
}

const (
	pollInterval     = 3 * time.Second
	fullRefreshEvery = 10  // cycles; ~every 30s re-pull everything to catch late edits
	offsetEMAAlpha   = 0.3 // smoothing weight for the per-source clock offset
)
