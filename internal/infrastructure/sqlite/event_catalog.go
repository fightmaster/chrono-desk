package sqlite

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// EventCatalog owns the event-file directory and lazily opens one SQLite store
// per event, caching the handles for the process lifetime.
type EventCatalog struct {
	dataDir string
	origin  *installationOrigin

	mu     sync.Mutex
	stores map[string]*Store
}

var unsafeEventFileChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func NewEventCatalog(dataDir string) (*EventCatalog, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir %s: %w", dataDir, err)
	}
	origin, err := loadOrCreateInstallationOrigin(dataDir)
	if err != nil {
		return nil, err
	}
	return &EventCatalog{dataDir: dataDir, origin: origin, stores: map[string]*Store{}}, nil
}

func (c *EventCatalog) eventPath(eventID string) string {
	name := unsafeEventFileChars.ReplaceAllString(eventID, "_")
	return filepath.Join(c.dataDir, name+".chrono")
}

// Open returns the store for an existing event file.
func (c *EventCatalog) Open(eventID string) (*Store, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.openLocked(eventID, false)
}

// OpenOrCreate returns the store for an event file, creating it on first use.
func (c *EventCatalog) OpenOrCreate(eventID string) (*Store, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.openLocked(eventID, true)
}

func (c *EventCatalog) openLocked(eventID string, create bool) (*Store, error) {
	if store, ok := c.stores[eventID]; ok {
		return store, nil
	}
	path := c.eventPath(eventID)
	if !create {
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("event %s: %w", eventID, err)
		}
	}
	db, err := Open(path)
	if err != nil {
		return nil, err
	}
	store, err := New(db, WithObservationOrigin(c.origin.instanceID, c.origin.Next))
	if err != nil {
		db.Close()
		return nil, err
	}
	c.stores[eventID] = store
	return store, nil
}

// ListEventIDs scans the data directory and returns known event ids.
func (c *EventCatalog) ListEventIDs() ([]string, error) {
	entries, err := os.ReadDir(c.dataDir)
	if err != nil {
		return nil, fmt.Errorf("read data dir: %w", err)
	}

	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".chrono") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(e.Name(), ".chrono"))
	}
	sort.Strings(ids)
	return ids, nil
}

// Close releases every open event database.
func (c *EventCatalog) Close() error {
	c.mu.Lock()
	stores := c.stores
	c.stores = map[string]*Store{}
	c.mu.Unlock()

	var firstErr error
	for id, store := range stores {
		if err := store.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close event %s: %w", id, err)
		}
	}
	if err := c.origin.Close(); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("close observation origin: %w", err)
	}
	return firstErr
}
