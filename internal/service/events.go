package service

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

// EventManager owns the data directory: one .chrono SQLite file per event,
// opened lazily and cached for the app's lifetime.
type EventManager struct {
	dataDir string
	logger  *log.Logger

	mu     sync.Mutex
	stores map[string]*sqlite.Store
}

func NewEventManager(dataDir string, logger *log.Logger) (*EventManager, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir %s: %w", dataDir, err)
	}
	return &EventManager{
		dataDir: dataDir,
		logger:  logger,
		stores:  map[string]*sqlite.Store{},
	}, nil
}

// EventInfo is a list entry for the UI.
type EventInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Date     string `json:"date"`
	Timezone string `json:"timezone"`
	File     string `json:"file"`
}

var unsafeFileChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func (m *EventManager) eventPath(eventID string) string {
	name := unsafeFileChars.ReplaceAllString(eventID, "_")
	return filepath.Join(m.dataDir, name+".chrono")
}

// Open returns the store for an existing event file.
func (m *EventManager) Open(eventID string) (*sqlite.Store, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.openLocked(eventID, false)
}

func (m *EventManager) openLocked(eventID string, create bool) (*sqlite.Store, error) {
	if store, ok := m.stores[eventID]; ok {
		return store, nil
	}
	path := m.eventPath(eventID)
	if !create {
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("event %s: %w", eventID, err)
		}
	}
	db, err := sqlite.Open(path)
	if err != nil {
		return nil, err
	}
	store, err := sqlite.New(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	m.stores[eventID] = store
	return store, nil
}

// ImportExport parses a run5 event export and applies it to the event's
// database file, creating the file on first import.
func (m *EventManager) ImportExport(ctx context.Context, r io.Reader) (ImportStats, error) {
	export, err := ParseEventExport(r)
	if err != nil {
		return ImportStats{}, err
	}

	m.mu.Lock()
	store, err := m.openLocked(export.Event.ID, true)
	m.mu.Unlock()
	if err != nil {
		return ImportStats{}, err
	}

	return NewEventImporter(store).Import(ctx, export)
}

// List scans the data directory and reads each event's header row.
func (m *EventManager) List(ctx context.Context) ([]EventInfo, error) {
	entries, err := os.ReadDir(m.dataDir)
	if err != nil {
		return nil, fmt.Errorf("read data dir: %w", err)
	}

	var infos []EventInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".chrono") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".chrono")
		store, err := m.Open(id)
		if err != nil {
			m.logger.Printf("skip event file %s: %v", e.Name(), err)
			continue
		}
		event, err := firstEvent(ctx, store)
		if err != nil {
			m.logger.Printf("skip event file %s: %v", e.Name(), err)
			continue
		}
		infos = append(infos, EventInfo{
			ID: event.ID, Name: event.Name, Date: event.Date, Timezone: event.Timezone, File: e.Name(),
		})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Date > infos[j].Date })
	return infos, nil
}

func firstEvent(ctx context.Context, store *sqlite.Store) (domain.Event, error) {
	var e domain.Event
	err := store.DB().QueryRowContext(ctx,
		`SELECT id, name, slug, date, timezone FROM events LIMIT 1`).
		Scan(&e.ID, &e.Name, &e.Slug, &e.Date, &e.Timezone)
	if err != nil {
		return domain.Event{}, fmt.Errorf("read event header: %w", err)
	}
	return e, nil
}

// Close releases all open event databases.
func (m *EventManager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, store := range m.stores {
		if err := store.DB().Close(); err != nil {
			m.logger.Printf("close event %s: %v", id, err)
		}
		delete(m.stores, id)
	}
}
