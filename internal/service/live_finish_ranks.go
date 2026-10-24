package service

import (
	"context"
	"database/sql"
	"sync"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/ranking"
)

type liveFinishPlaces struct {
	Gender   *int
	Category *int
}

type liveFinishRaceKey struct{ EventID, RaceID string }
type liveFinishRankEntry struct {
	database *sql.DB
	revision sqlite.LiveFinishRankRevision
	places   map[string]liveFinishPlaces
}

// One immutable result per race/revision, shared by all presenter clients.
// Raw observation inserts do not invalidate it. No replay or BuildProtocol.
type liveFinishRankCache struct {
	mu     sync.Mutex
	races  map[liveFinishRaceKey]liveFinishRankEntry
	builds uint64
}

func (c *liveFinishRankCache) places(ctx context.Context, store *sqlite.Store, race domain.Race, revision sqlite.LiveFinishRankRevision) (map[string]liveFinishPlaces, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := liveFinishRaceKey{EventID: race.EventID, RaceID: race.ID}
	cached, exists := c.races[key]
	if exists && cached.database == store.DB() && cached.revision == revision {
		return cached.places, nil
	}
	members, err := store.ListMembersByRace(ctx, race.ID)
	if err != nil {
		return nil, err
	}
	places := make(map[string]liveFinishPlaces)
	for _, row := range ranking.Protocol(race, members, nil) {
		places[row.Member.ID] = liveFinishPlaces{Gender: row.GenderPlace, Category: row.CategoryPlace}
	}
	c.builds++
	// An older pinned snapshot must not evict a newer cache entry.
	newer := exists && cached.database == store.DB() && (cached.revision.Config > revision.Config || cached.revision.Eligibility > revision.Eligibility)
	if !newer {
		if c.races == nil {
			c.races = make(map[liveFinishRaceKey]liveFinishRankEntry)
		}
		c.races[key] = liveFinishRankEntry{database: store.DB(), revision: revision, places: places}
	}
	return places, nil
}
