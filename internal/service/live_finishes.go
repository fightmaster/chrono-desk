package service

import (
	"context"
	"errors"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/finishchange"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

var ErrLiveFinishRace = errors.New("дистанция недоступна в трансляции финишей")

// FinishChanges gives the public view its store-lifetime commit signal.
func (s *EventService) FinishChanges(eventID string) (*finishchange.Signal, error) {
	store, err := s.catalog.Open(eventID)
	if err != nil {
		return nil, err
	}
	return store.FinishChanges(), nil
}

type LiveFinishSnapshot struct {
	Event    domain.Event
	Races    []domain.Race
	Finishes []sqlite.LiveFinish
}

// LiveFinishes reads one committed state; it never enters a writer transaction.
func (s *EventService) LiveFinishes(ctx context.Context, eventID, raceID string) (LiveFinishSnapshot, error) {
	var snapshot LiveFinishSnapshot
	err := s.WithReadSnapshot(ctx, eventID, func(store *sqlite.Store) error {
		var err error
		snapshot.Event, err = FirstEvent(ctx, store)
		if err != nil {
			return err
		}
		races, err := store.ListRaces(ctx, eventID)
		if err != nil {
			return err
		}
		snapshot.Races = make([]domain.Race, 0)
		valid := raceID == ""
		for _, race := range races {
			if race.Format == domain.FormatFixedDistance {
				snapshot.Races = append(snapshot.Races, race)
				valid = valid || race.ID == raceID
			}
		}
		if !valid {
			return ErrLiveFinishRace
		}
		snapshot.Finishes, err = store.LatestFinishes(ctx, eventID, raceID)
		if err != nil || len(snapshot.Finishes) == 0 {
			return err
		}
		revision, err := store.LiveFinishRankRevision(ctx, eventID)
		if err != nil {
			return err
		}
		byRace := make(map[string]map[string]liveFinishPlaces)
		for _, race := range snapshot.Races {
			for _, finish := range snapshot.Finishes {
				if finish.RaceID == race.ID {
					byRace[race.ID], err = s.finishRanks.places(ctx, store, race, revision)
					if err != nil {
						return err
					}
					break
				}
			}
		}
		for i := range snapshot.Finishes {
			finish := &snapshot.Finishes[i]
			places := byRace[finish.RaceID][finish.MemberID]
			finish.GenderPlace, finish.CategoryPlace = places.Gender, places.Category
		}
		return nil
	})
	return snapshot, err
}
