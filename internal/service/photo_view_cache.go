package service

import (
	"context"
	"encoding/json"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

type photoMergeStore interface {
	PhotoMetadataVersion(context.Context, string) (sqlite.PhotoMetadataVersion, error)
	ListPhotosForMerge(context.Context, string) ([]sqlite.Photo, error)
	GetFramesByIDs(context.Context, string, []string) (map[string]json.RawMessage, error)
}

type photoViewKey struct {
	eventID string
	window  int64
}

type photoViewEntry struct {
	version sqlite.PhotoMetadataVersion
	photos  int
	merged  []MergedFinish
	used    uint64
}

// PhotoViewCache shares merge work between list/status requests. Callers must
// pass a coherent read snapshot, including version, metadata and visible frames.
// A bounded context-aware gate coalesces concurrent misses; writers never use it.
// Only metadata is retained: image bytes and burst JSON are not cached here.
type PhotoViewCache struct {
	gate    chan struct{}
	entries map[photoViewKey]photoViewEntry
	clock   uint64
}

func NewPhotoViewCache() *PhotoViewCache {
	return &PhotoViewCache{gate: make(chan struct{}, 1), entries: make(map[photoViewKey]photoViewEntry)}
}

func (c *PhotoViewCache) load(ctx context.Context, store photoMergeStore, eventID string, windowMs int64) (photoViewEntry, error) {
	if err := ctx.Err(); err != nil {
		return photoViewEntry{}, err
	}
	// Read the token inside the caller's snapshot before consulting the cache.
	// An older reader can safely reuse only its own exact committed version.
	version, err := store.PhotoMetadataVersion(ctx, eventID)
	if err != nil {
		return photoViewEntry{}, err
	}
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	case <-ctx.Done():
		return photoViewEntry{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return photoViewEntry{}, err
	}
	key := photoViewKey{eventID: eventID, window: windowMs}
	entry, found := c.entries[key]
	c.clock++
	if found && entry.version == version {
		entry.used = c.clock
		c.entries[key] = entry
		return entry, nil
	}
	photos, err := store.ListPhotosForMerge(ctx, eventID)
	if err != nil {
		return photoViewEntry{}, err
	}
	entry = photoViewEntry{version: version, photos: len(photos), merged: MergeFinishes(photos, windowMs), used: c.clock}
	if err := ctx.Err(); err != nil {
		return photoViewEntry{}, err
	}
	// Keep at most four event/window projections, including custom merge windows.
	// Navigating across old events cannot retain an unbounded history of views.
	if !found && len(c.entries) >= 4 {
		var oldest photoViewKey
		oldestUse := ^uint64(0)
		for candidate, value := range c.entries {
			if value.used < oldestUse {
				oldest, oldestUse = candidate, value.used
			}
		}
		delete(c.entries, oldest)
	}
	c.entries[key] = entry
	return entry, nil
}

func (c *PhotoViewCache) Counts(ctx context.Context, store photoMergeStore, eventID string) (photos, finishes int, err error) {
	entry, err := c.load(ctx, store, eventID, MergeWindowMs)
	return entry.photos, len(entry.merged), err
}

func (c *PhotoViewCache) Merged(ctx context.Context, store photoMergeStore, eventID string, windowMs int64, limit int) ([]MergedFinish, error) {
	entry, err := c.load(ctx, store, eventID, windowMs)
	if err != nil {
		return nil, err
	}
	count := len(entry.merged)
	if limit > 0 && count > limit {
		count = limit
	}
	// Do not attach current frame JSON to cached entries or expose mutable slices.
	finishes := make([]MergedFinish, count)
	copy(finishes, entry.merged[:count])
	for i := range finishes {
		finishes[i].Cams = append(make([]string, 0, len(finishes[i].Cams)), finishes[i].Cams...)
	}
	return hydrateMergedFrames(ctx, store, eventID, finishes)
}
