package sqlite

import (
	"context"
	"fmt"
	"strconv"
	"strings"

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

// CountRacesAndMembers returns the race and member tallies for the event list
// card. One event per file, so the counts are unfiltered — two cheap COUNTs
// instead of the client fetching full lists for their length.
func (s *Store) CountRacesAndMembers(ctx context.Context) (races, members int, err error) {
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM races`).Scan(&races); err != nil {
		return 0, 0, fmt.Errorf("count races: %w", err)
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM members`).Scan(&members); err != nil {
		return 0, 0, fmt.Errorf("count members: %w", err)
	}
	return races, members, nil
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

func (s *Store) GetMember(ctx context.Context, memberID string) (domain.Member, error) {
	var m domain.Member
	var status int
	err := s.db.QueryRowContext(ctx, `
		SELECT id, event_id, race_id, category_id, number, epc, rfid, first_name, last_name,
			gender, dob, city, team, status, start_time_ms, finish_time_ms, clean_time
		FROM members WHERE id = ?`, memberID).
		Scan(&m.ID, &m.EventID, &m.RaceID, &m.CategoryID, &m.Number, &m.EPC, &m.RFID,
			&m.FirstName, &m.LastName, &m.Gender, &m.DOB, &m.City, &m.Team, &status,
			&m.StartTimeMs, &m.FinishTimeMs, &m.CleanTime)
	if err != nil {
		return domain.Member{}, fmt.Errorf("get member %s: %w", memberID, err)
	}
	m.Status = domain.MemberStatus(status)
	return m, nil
}

// MemberRow is the slim JSON shape for the searchable members list.
type MemberRow struct {
	ID         string  `json:"id"`
	RaceID     string  `json:"race_id"`
	Number     *int64  `json:"number"`
	FirstName  string  `json:"first_name"`
	LastName   string  `json:"last_name"`
	EPC        *string `json:"epc"`
	CategoryID *string `json:"category_id"`
	Status     int     `json:"status"`
}

func (s *Store) ListMembersByEvent(ctx context.Context, eventID string) ([]MemberRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, race_id, number, first_name, last_name, epc, category_id, status
		FROM members WHERE event_id = ? ORDER BY last_name, first_name`, eventID)
	if err != nil {
		return nil, fmt.Errorf("list event members: %w", err)
	}
	defer rows.Close()

	out := []MemberRow{}
	for rows.Next() {
		var m MemberRow
		if err := rows.Scan(&m.ID, &m.RaceID, &m.Number, &m.FirstName, &m.LastName, &m.EPC, &m.CategoryID, &m.Status); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) GetCheckpoint(ctx context.Context, id string) (CheckpointRow, error) {
	var cp CheckpointRow
	err := s.db.QueryRowContext(ctx, `
		SELECT id, race_id, name, type, sort, board, since_ms, since_offset_seconds, sleep_after_prev_seconds
		FROM checkpoints WHERE id = ?`, id).
		Scan(&cp.ID, &cp.RaceID, &cp.Name, &cp.Type, &cp.Sort, &cp.Board,
			&cp.SinceMs, &cp.SinceOffsetSeconds, &cp.SleepAfterPrevSeconds)
	if err != nil {
		return CheckpointRow{}, fmt.Errorf("get checkpoint %s: %w", id, err)
	}
	return cp, nil
}

// DeleteCheckpointCascade removes the checkpoint together with the results it
// produced (the caller recounts afterwards). No-op when already absent.
func (s *Store) DeleteCheckpointCascade(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	if _, err := tx.ExecContext(ctx, `DELETE FROM results WHERE checkpoint_id = ?`, id); err != nil {
		return fmt.Errorf("delete checkpoint results: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM checkpoints WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete checkpoint: %w", err)
	}
	return tx.Commit()
}

func (s *Store) ListLaps(ctx context.Context) ([]domain.Lap, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, slug, description FROM laps ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list laps: %w", err)
	}
	defer rows.Close()

	var laps []domain.Lap
	for rows.Next() {
		var l domain.Lap
		if err := rows.Scan(&l.ID, &l.Name, &l.Slug, &l.Description); err != nil {
			return nil, err
		}
		laps = append(laps, l)
	}
	return laps, rows.Err()
}

func (s *Store) ListMembersFullByEvent(ctx context.Context, eventID string) ([]domain.Member, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, event_id, race_id, category_id, number, epc, rfid, first_name, last_name,
			gender, dob, city, team, status, start_time_ms, finish_time_ms, clean_time
		FROM members WHERE event_id = ? ORDER BY id`, eventID)
	if err != nil {
		return nil, fmt.Errorf("list event members full: %w", err)
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

func (s *Store) ListCheckpointsFullByEvent(ctx context.Context, eventID string) ([]domain.Checkpoint, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, event_id, race_id, name, type, sort, board, since_ms, since_offset_seconds, sleep_after_prev_seconds
		FROM checkpoints WHERE event_id = ? ORDER BY race_id, sort`, eventID)
	if err != nil {
		return nil, fmt.Errorf("list event checkpoints: %w", err)
	}
	defer rows.Close()

	var checkpoints []domain.Checkpoint
	for rows.Next() {
		var cp domain.Checkpoint
		var cpType int
		if err := rows.Scan(&cp.ID, &cp.EventID, &cp.RaceID, &cp.Name, &cpType, &cp.Sort, &cp.Board,
			&cp.SinceMs, &cp.SinceOffsetSeconds, &cp.SleepAfterPrevSeconds); err != nil {
			return nil, err
		}
		cp.Type = domain.CheckpointType(cpType)
		checkpoints = append(checkpoints, cp)
	}
	return checkpoints, rows.Err()
}

// SnapshotTo writes a consistent copy of the live database (VACUUM INTO works
// safely alongside WAL) — the .chrono backup primitive.
func (s *Store) SnapshotTo(ctx context.Context, path string) error {
	escaped := strings.ReplaceAll(path, "'", "''")
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf("VACUUM INTO '%s'", escaped)); err != nil {
		return fmt.Errorf("snapshot to %s: %w", path, err)
	}
	return nil
}
