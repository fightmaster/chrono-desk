package sqlite

import (
	"context"
	"fmt"
)

// InsertManualResult records a judge-entered crossing: no rfid log, no
// checkpoint. Manual rows survive recount wipes by design.
func (s *Store) InsertManualResult(ctx context.Context, eventID, raceID, memberID string, timeMs int64, number *int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO results (event_id, race_id, member_id, checkpoint_id, rfid_log_id, time_ms, number)
		VALUES (?, ?, ?, NULL, NULL, ?, ?)`,
		eventID, raceID, memberID, timeMs, number)
	if err != nil {
		return 0, fmt.Errorf("insert manual result: %w", err)
	}
	return res.LastInsertId()
}

// ManualResult is one judge-entered crossing.
type ManualResult struct {
	ID       int64  `json:"id"`
	MemberID string `json:"member_id"`
	RaceID   string `json:"race_id"`
	TimeMs   int64  `json:"time_ms"`
}

// ListManualResults returns judge entries in chronological order, optionally
// race-scoped.
func (s *Store) ListManualResults(ctx context.Context, eventID, raceID string) ([]ManualResult, error) {
	query := `SELECT id, member_id, race_id, time_ms FROM results
		WHERE event_id = ? AND rfid_log_id IS NULL AND checkpoint_id IS NULL`
	args := []any{eventID}
	if raceID != "" {
		query += ` AND race_id = ?`
		args = append(args, raceID)
	}
	query += ` ORDER BY time_ms, id`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list manual results: %w", err)
	}
	defer rows.Close()

	var out []ManualResult
	for rows.Next() {
		var m ManualResult
		if err := rows.Scan(&m.ID, &m.MemberID, &m.RaceID, &m.TimeMs); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ManualResultDetail is a judge entry joined with the participant, for the
// review/undo list on the live screen.
type ManualResultDetail struct {
	ID        int64   `json:"id"`
	MemberID  string  `json:"member_id"`
	Number    *int64  `json:"number"`
	FirstName string  `json:"first_name"`
	LastName  string  `json:"last_name"`
	TimeMs    int64   `json:"time_ms"`
	CleanTime *string `json:"clean_time"`
}

// ListManualResultsDetailed returns judge entries with participant name/number
// and the member's derived clean time, newest first.
func (s *Store) ListManualResultsDetailed(ctx context.Context, eventID, raceID string) ([]ManualResultDetail, error) {
	query := `SELECT r.id, r.member_id, m.number, m.first_name, m.last_name, r.time_ms, m.clean_time
		FROM results r
		JOIN members m ON m.id = r.member_id
		WHERE r.event_id = ? AND r.rfid_log_id IS NULL AND r.checkpoint_id IS NULL`
	args := []any{eventID}
	if raceID != "" {
		query += ` AND r.race_id = ?`
		args = append(args, raceID)
	}
	query += ` ORDER BY r.time_ms DESC, r.id DESC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list manual results detailed: %w", err)
	}
	defer rows.Close()

	out := []ManualResultDetail{}
	for rows.Next() {
		var d ManualResultDetail
		if err := rows.Scan(&d.ID, &d.MemberID, &d.Number, &d.FirstName, &d.LastName, &d.TimeMs, &d.CleanTime); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetManualResult fetches one judge entry by id (for journaling its natural key
// before deletion, so the deletion can be synced to the site).
func (s *Store) GetManualResult(ctx context.Context, eventID string, resultID int64) (ManualResult, error) {
	var m ManualResult
	err := s.db.QueryRowContext(ctx, `
		SELECT id, member_id, race_id, time_ms FROM results
		WHERE id = ? AND event_id = ? AND rfid_log_id IS NULL AND checkpoint_id IS NULL`,
		resultID, eventID).Scan(&m.ID, &m.MemberID, &m.RaceID, &m.TimeMs)
	if err != nil {
		return ManualResult{}, fmt.Errorf("get manual result %d: %w", resultID, err)
	}
	return m, nil
}

// DeleteManualResult removes a judge entry; refuses to touch derived rows.
func (s *Store) DeleteManualResult(ctx context.Context, eventID string, resultID int64) error {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM results WHERE id = ? AND event_id = ? AND rfid_log_id IS NULL AND checkpoint_id IS NULL`,
		resultID, eventID)
	if err != nil {
		return fmt.Errorf("delete manual result: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("ручной результат %d не найден", resultID)
	}
	return nil
}

// LivePass is one feed row for the finish-judge screen: the read plus what it
// resolved to. Manual judge finishes share this shape so they appear inline in
// the feed (Manual=true, ResultID set) instead of a separate list.
type LivePass struct {
	LogID          string  `json:"log_id"`
	TimeMs         int64   `json:"time_ms"`
	EPC            string  `json:"epc"`
	Board          string  `json:"board"`
	DisabledAt     *int64  `json:"disabled_at"`
	MemberID       *string `json:"member_id"`
	Number         *int64  `json:"number"`
	FirstName      *string `json:"first_name"`
	LastName       *string `json:"last_name"`
	RaceID         *string `json:"race_id"`
	CheckpointName *string `json:"checkpoint_name"` // nil → read produced no result
	CheckpointType *int    `json:"checkpoint_type"`
	ResultID       *int64  `json:"result_id"` // set only for manual entries (inline delete)
	Manual         bool    `json:"manual"`
}

// ListRecentPasses feeds the live screen: latest reads, who they belong to and
// whether they produced a result, plus every manual judge finish so the judge
// sees and can delete them inline. The chip-read window is capped by limit;
// manual entries are always appended (they are few) so they never sink out of
// view, then the whole feed is ordered by time.
func (s *Store) ListRecentPasses(ctx context.Context, eventID string, limit int) ([]LivePass, error) {
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT log_id, time_ms, epc, board, disabled_at,
		       member_id, number, first_name, last_name, race_id,
		       checkpoint_name, checkpoint_type, result_id, manual
		FROM (
			SELECT l.id AS log_id, l.time_ms AS time_ms, l.epc AS epc, l.board AS board,
			       l.disabled_at AS disabled_at, m.id AS member_id, m.number AS number,
			       m.first_name AS first_name, m.last_name AS last_name, m.race_id AS race_id,
			       c.name AS checkpoint_name, c.type AS checkpoint_type,
			       NULL AS result_id, 0 AS manual
			FROM rfid_logs l
			LEFT JOIN members m ON m.event_id = l.event_id AND m.epc = l.epc
			LEFT JOIN results r ON r.rfid_log_id = l.id
			LEFT JOIN checkpoints c ON c.id = r.checkpoint_id
			WHERE l.event_id = ?
			ORDER BY l.time_ms DESC, l.id DESC
			LIMIT ?
		)
		UNION ALL
		SELECT 'manual:' || r.id, r.time_ms, '', '', NULL,
		       m.id, m.number, m.first_name, m.last_name, m.race_id,
		       NULL, NULL, r.id, 1
		FROM results r
		JOIN members m ON m.id = r.member_id
		WHERE r.event_id = ? AND r.rfid_log_id IS NULL AND r.checkpoint_id IS NULL
		ORDER BY time_ms DESC, log_id DESC`, eventID, limit, eventID)
	if err != nil {
		return nil, fmt.Errorf("list recent passes: %w", err)
	}
	defer rows.Close()

	out := []LivePass{}
	for rows.Next() {
		var (
			p      LivePass
			manual int
		)
		if err := rows.Scan(&p.LogID, &p.TimeMs, &p.EPC, &p.Board, &p.DisabledAt,
			&p.MemberID, &p.Number, &p.FirstName, &p.LastName, &p.RaceID,
			&p.CheckpointName, &p.CheckpointType, &p.ResultID, &manual); err != nil {
			return nil, err
		}
		p.Manual = manual == 1
		out = append(out, p)
	}
	return out, rows.Err()
}
