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
// resolved to.
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
}

// ListRecentPasses feeds the live screen: latest reads, who they belong to
// and whether they produced a result.
func (s *Store) ListRecentPasses(ctx context.Context, eventID string, limit int) ([]LivePass, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.id, l.time_ms, l.epc, l.board, l.disabled_at,
		       m.id, m.number, m.first_name, m.last_name, m.race_id,
		       c.name, c.type
		FROM rfid_logs l
		LEFT JOIN members m ON m.event_id = l.event_id AND m.epc = l.epc
		LEFT JOIN results r ON r.rfid_log_id = l.id
		LEFT JOIN checkpoints c ON c.id = r.checkpoint_id
		WHERE l.event_id = ?
		ORDER BY l.time_ms DESC, l.id DESC
		LIMIT ?`, eventID, limit)
	if err != nil {
		return nil, fmt.Errorf("list recent passes: %w", err)
	}
	defer rows.Close()

	out := []LivePass{}
	for rows.Next() {
		var p LivePass
		if err := rows.Scan(&p.LogID, &p.TimeMs, &p.EPC, &p.Board, &p.DisabledAt,
			&p.MemberID, &p.Number, &p.FirstName, &p.LastName, &p.RaceID,
			&p.CheckpointName, &p.CheckpointType); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
