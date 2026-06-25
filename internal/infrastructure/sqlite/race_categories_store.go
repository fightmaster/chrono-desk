package sqlite

import (
	"context"
	"fmt"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

// race_categories is run5's category_race pivot: which catalog categories are
// attached to each race. The attached set (not the global catalog) is what the
// member-edit dropdown offers; it is journaled and synced back to the site.

// CategoryRace is one pivot row (race ↔ category attachment).
type CategoryRace struct {
	RaceID     string `json:"race_id"`
	CategoryID string `json:"category_id"`
}

// AttachRaceCategory attaches a category to a race (idempotent).
func (s *Store) AttachRaceCategory(ctx context.Context, raceID, categoryID string) error {
	return upsertRaceCategory(ctx, s.db, raceID, categoryID)
}

func upsertRaceCategory(ctx context.Context, ex execer, raceID, categoryID string) error {
	_, err := ex.ExecContext(ctx,
		`INSERT INTO race_categories (race_id, category_id) VALUES (?, ?)
		 ON CONFLICT(race_id, category_id) DO NOTHING`, raceID, categoryID)
	if err != nil {
		return fmt.Errorf("attach category %s to race %s: %w", categoryID, raceID, err)
	}
	return nil
}

// DetachRaceCategory removes a category from a race (idempotent — no-op when
// already absent).
func (s *Store) DetachRaceCategory(ctx context.Context, raceID, categoryID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM race_categories WHERE race_id = ? AND category_id = ?`, raceID, categoryID)
	if err != nil {
		return fmt.Errorf("detach category %s from race %s: %w", categoryID, raceID, err)
	}
	return nil
}

// ListRaceCategories returns the categories attached to a race, ordered by name
// (the per-race set the member dropdown and chips render).
func (s *Store) ListRaceCategories(ctx context.Context, raceID string) ([]domain.Category, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.name, c.min, c.max, c.gender
		FROM race_categories rc JOIN categories c ON c.id = rc.category_id
		WHERE rc.race_id = ? ORDER BY c.name`, raceID)
	if err != nil {
		return nil, fmt.Errorf("list race categories: %w", err)
	}
	defer rows.Close()

	categories := make([]domain.Category, 0)
	for rows.Next() {
		var c domain.Category
		if err := rows.Scan(&c.ID, &c.Name, &c.Min, &c.Max, &c.Gender); err != nil {
			return nil, err
		}
		categories = append(categories, c)
	}
	return categories, rows.Err()
}

// ListCategoryRaces returns the pivot rows for the event's races, deterministically
// ordered (export + sync need byte-identical output for idempotent re-push).
func (s *Store) ListCategoryRaces(ctx context.Context, eventID string) ([]CategoryRace, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT rc.race_id, rc.category_id
		FROM race_categories rc JOIN races r ON r.id = rc.race_id
		WHERE r.event_id = ? ORDER BY rc.race_id, rc.category_id`, eventID)
	if err != nil {
		return nil, fmt.Errorf("list category races: %w", err)
	}
	defer rows.Close()

	var pivot []CategoryRace
	for rows.Next() {
		var cr CategoryRace
		if err := rows.Scan(&cr.RaceID, &cr.CategoryID); err != nil {
			return nil, err
		}
		pivot = append(pivot, cr)
	}
	return pivot, rows.Err()
}

// CountMembersInRaceCategory counts members of a race assigned to a category —
// used to guard a detach (don't strand assigned participants).
func (s *Store) CountMembersInRaceCategory(ctx context.Context, raceID, categoryID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM members WHERE race_id = ? AND category_id = ?`, raceID, categoryID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count members in race category: %w", err)
	}
	return n, nil
}
