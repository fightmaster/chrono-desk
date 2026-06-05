package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/ranking"
)

// LastPassesInWindow returns each member's final result inside their personal
// time-limited window [start_time, start_time + limit]. Mirrors rfid-sync's
// LoadLastPassInWindow query (ORDER BY time_ms DESC, id DESC) applied per
// member of the race.
func (s *Store) LastPassesInWindow(ctx context.Context, race domain.Race, members []domain.Member) (map[string]ranking.LastPass, error) {
	if race.TimeLimitSeconds == nil || *race.TimeLimitSeconds <= 0 {
		return map[string]ranking.LastPass{}, nil
	}

	stmt, err := s.db.PrepareContext(ctx, `
		SELECT r.time_ms, c.sort, c.name
		FROM results r LEFT JOIN checkpoints c ON c.id = r.checkpoint_id
		WHERE r.race_id = ? AND r.member_id = ? AND r.time_ms BETWEEN ? AND ?
		ORDER BY r.time_ms DESC, r.id DESC
		LIMIT 1`)
	if err != nil {
		return nil, fmt.Errorf("prepare last pass: %w", err)
	}
	defer stmt.Close()

	passes := make(map[string]ranking.LastPass)
	for _, m := range members {
		if m.StartTimeMs == nil {
			continue
		}
		windowStart := *m.StartTimeMs
		windowEnd := windowStart + *race.TimeLimitSeconds*1000

		var pass ranking.LastPass
		var sort sql.NullInt64
		var name sql.NullString
		err := stmt.QueryRowContext(ctx, race.ID, m.ID, windowStart, windowEnd).
			Scan(&pass.TimeMs, &sort, &name)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("last pass for member %s: %w", m.ID, err)
		}
		pass.CheckpointSort = nullableInt64(sort)
		if name.Valid {
			n := name.String
			pass.CheckpointName = &n
		}
		passes[m.ID] = pass
	}
	return passes, nil
}
