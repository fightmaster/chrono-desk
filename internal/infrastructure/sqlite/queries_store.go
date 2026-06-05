package sqlite

import (
	"context"
	"fmt"
	"strconv"

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

// ExistingRfidLogKeys returns the content keys (epc|time_ms|ant) of logs
// already stored for a board — the flash-import dedup set (run5's
// loadExistingKeys analog; legacy rows may carry non-formula ids).
func (s *Store) ExistingRfidLogKeys(ctx context.Context, eventID, board string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT epc, time_ms, ant FROM rfid_logs WHERE event_id = ? AND board = ?`, eventID, board)
	if err != nil {
		return nil, fmt.Errorf("existing log keys: %w", err)
	}
	defer rows.Close()

	keys := make(map[string]bool)
	for rows.Next() {
		var epc string
		var timeMs int64
		var ant int
		if err := rows.Scan(&epc, &timeMs, &ant); err != nil {
			return nil, err
		}
		keys[epc+"|"+strconv.FormatInt(timeMs, 10)+"|"+strconv.Itoa(ant)] = true
	}
	return keys, rows.Err()
}

// CheckpointRow is the JSON shape for the checkpoint editor.
type CheckpointRow struct {
	ID                    string `json:"id"`
	RaceID                string `json:"race_id"`
	Name                  string `json:"name"`
	Type                  int    `json:"type"`
	Sort                  int64  `json:"sort"`
	Board                 string `json:"board"`
	SinceMs               *int64 `json:"since_ms"`
	SinceOffsetSeconds    *int64 `json:"since_offset_seconds"`
	SleepAfterPrevSeconds *int64 `json:"sleep_after_prev_seconds"`
}

func (s *Store) ListCheckpointsByEvent(ctx context.Context, eventID string) ([]CheckpointRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, race_id, name, type, sort, board, since_ms, since_offset_seconds, sleep_after_prev_seconds
		FROM checkpoints WHERE event_id = ? ORDER BY race_id, sort`, eventID)
	if err != nil {
		return nil, fmt.Errorf("list checkpoints: %w", err)
	}
	defer rows.Close()

	out := []CheckpointRow{}
	for rows.Next() {
		var cp CheckpointRow
		if err := rows.Scan(&cp.ID, &cp.RaceID, &cp.Name, &cp.Type, &cp.Sort, &cp.Board,
			&cp.SinceMs, &cp.SinceOffsetSeconds, &cp.SleepAfterPrevSeconds); err != nil {
			return nil, err
		}
		out = append(out, cp)
	}
	return out, rows.Err()
}
