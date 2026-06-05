package sqlite

import (
	"context"
	"fmt"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

func (s *Store) ListRaces(ctx context.Context, eventID string) ([]domain.Race, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, event_id, name, date, started_at_ms, lap_id, format, time_limit_seconds, category_excludes_top_by_gender
		FROM races WHERE event_id = ? ORDER BY name`, eventID)
	if err != nil {
		return nil, fmt.Errorf("list races: %w", err)
	}
	defer rows.Close()

	var races []domain.Race
	for rows.Next() {
		var r domain.Race
		var format string
		if err := rows.Scan(&r.ID, &r.EventID, &r.Name, &r.Date, &r.StartedAtMs, &r.LapID,
			&format, &r.TimeLimitSeconds, &r.CategoryExcludesTopByGender); err != nil {
			return nil, err
		}
		r.Format = domain.RaceFormat(format)
		races = append(races, r)
	}
	return races, rows.Err()
}

func (s *Store) GetRace(ctx context.Context, raceID string) (domain.Race, error) {
	var r domain.Race
	var format string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, event_id, name, date, started_at_ms, lap_id, format, time_limit_seconds, category_excludes_top_by_gender
		FROM races WHERE id = ?`, raceID).
		Scan(&r.ID, &r.EventID, &r.Name, &r.Date, &r.StartedAtMs, &r.LapID,
			&format, &r.TimeLimitSeconds, &r.CategoryExcludesTopByGender)
	if err != nil {
		return domain.Race{}, fmt.Errorf("get race %s: %w", raceID, err)
	}
	r.Format = domain.RaceFormat(format)
	return r, nil
}

func (s *Store) ListMembersByRace(ctx context.Context, raceID string) ([]domain.Member, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, event_id, race_id, category_id, number, epc, rfid, first_name, last_name,
			gender, dob, city, team, status, start_time_ms, finish_time_ms, clean_time
		FROM members WHERE race_id = ? ORDER BY id`, raceID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()

	var members []domain.Member
	for rows.Next() {
		var m domain.Member
		var status int
		if err := rows.Scan(&m.ID, &m.EventID, &m.RaceID, &m.CategoryID, &m.Number, &m.EPC, &m.RFID,
			&m.FirstName, &m.LastName, &m.Gender, &m.DOB, &m.City, &m.Team, &status,
			&m.StartTimeMs, &m.FinishTimeMs, &m.CleanTime); err != nil {
			return nil, err
		}
		m.Status = domain.MemberStatus(status)
		members = append(members, m)
	}
	return members, rows.Err()
}

func (s *Store) ListCategories(ctx context.Context) (map[string]domain.Category, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, min, max, gender FROM categories`)
	if err != nil {
		return nil, fmt.Errorf("list categories: %w", err)
	}
	defer rows.Close()

	categories := make(map[string]domain.Category)
	for rows.Next() {
		var c domain.Category
		if err := rows.Scan(&c.ID, &c.Name, &c.Min, &c.Max, &c.Gender); err != nil {
			return nil, err
		}
		categories[c.ID] = c
	}
	return categories, rows.Err()
}
