package service

import (
	"context"
	"encoding/json"
	"fmt"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

// Per-race category attachment (run5's category_race pivot). The catalog of
// categories is event-global and site-owned; attaching/detaching one to a race
// is an offline edit, journaled (entity "race_category", pseudo-fields
// "_attached"/"_detached") so it survives re-imports (replayed in
// ReapplyLocalEdits) and syncs back to run5. The catalog itself is never
// created offline — a missing category is added on the site and re-imported.

// categoryRacePair is the JSON payload of a race_category journal entry and the
// shape carried in the sync payload.
type categoryRacePair struct {
	RaceID     string `json:"race_id"`
	CategoryID string `json:"category_id"`
}

// AttachCategory attaches a catalog category to a race and journals it.
func AttachCategory(ctx context.Context, store *sqlite.Store, eventID, raceID, categoryID string) (EditResult, error) {
	if err := validateRaceCategory(ctx, store, eventID, raceID, categoryID, true); err != nil {
		return EditResult{}, err
	}
	if err := store.AttachRaceCategory(ctx, raceID, categoryID); err != nil {
		return EditResult{}, err
	}
	if err := journalRaceCategory(ctx, store, "_attached", raceID, categoryID); err != nil {
		return EditResult{}, err
	}
	return EditResult{}, nil
}

// DetachCategory removes a category from a race. It refuses while members of the
// race are still assigned to it (don't strand participants — reassign first).
func DetachCategory(ctx context.Context, store *sqlite.Store, eventID, raceID, categoryID string) (EditResult, error) {
	if err := validateRaceCategory(ctx, store, eventID, raceID, categoryID, false); err != nil {
		return EditResult{}, err
	}
	n, err := store.CountMembersInRaceCategory(ctx, raceID, categoryID)
	if err != nil {
		return EditResult{}, err
	}
	if n > 0 {
		return EditResult{}, fmt.Errorf("в этой категории ещё %d участник(ов) — сначала переназначьте их", n)
	}
	if err := store.DetachRaceCategory(ctx, raceID, categoryID); err != nil {
		return EditResult{}, err
	}
	if err := journalRaceCategory(ctx, store, "_detached", raceID, categoryID); err != nil {
		return EditResult{}, err
	}
	return EditResult{}, nil
}

func validateRaceCategory(ctx context.Context, store *sqlite.Store, eventID, raceID, categoryID string, requireCatalog bool) error {
	race, err := store.GetRace(ctx, raceID)
	if err != nil || race.EventID != eventID {
		return fmt.Errorf("гонка %s не найдена в событии", raceID)
	}
	if requireCatalog {
		categories, err := store.ListCategories(ctx)
		if err != nil {
			return err
		}
		if _, ok := categories[categoryID]; !ok {
			return fmt.Errorf("категория %s не найдена в каталоге", categoryID)
		}
	}
	return nil
}

func journalRaceCategory(ctx context.Context, store *sqlite.Store, field, raceID, categoryID string) error {
	payload, err := json.Marshal(categoryRacePair{RaceID: raceID, CategoryID: categoryID})
	if err != nil {
		return fmt.Errorf("encode race_category: %w", err)
	}
	return store.InsertLocalChange(ctx, sqlite.LocalChange{
		Entity: "race_category", EntityID: raceID + ":" + categoryID, Field: field,
		OldValue: "null", NewValue: string(payload),
	})
}
