package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/ranking"
)

const lastPassesInWindowSQL = `
	WITH ranked AS (
		SELECT
			r.member_id,
			r.time_ms,
			c.sort,
			c.name,
			ROW_NUMBER() OVER (
				PARTITION BY r.member_id
				ORDER BY r.time_ms DESC, r.id DESC
			) AS position
		FROM members AS m INDEXED BY idx_members_race
		JOIN results AS r INDEXED BY idx_results_member_time
			ON r.member_id = m.id AND r.race_id = m.race_id
		LEFT JOIN checkpoints AS c ON c.id = r.checkpoint_id
		WHERE m.race_id = ?
			AND m.start_time_ms IS NOT NULL
			AND r.time_ms BETWEEN m.start_time_ms AND m.start_time_ms + ?
	)
	SELECT member_id, time_ms, sort, name
	FROM ranked
	WHERE position = 1`

// LastPassesInWindow returns each member's final result inside their personal
// time-limited window [start_time, start_time + limit]. One window query ranks
// all race members; protocol rendering therefore does not grow by one database
// round trip per participant.
func (s *Store) LastPassesInWindow(ctx context.Context, race domain.Race) (map[string]ranking.LastPass, error) {
	if race.TimeLimitSeconds == nil || *race.TimeLimitSeconds <= 0 {
		return map[string]ranking.LastPass{}, nil
	}

	rows, err := s.db.QueryContext(ctx, lastPassesInWindowSQL, race.ID, *race.TimeLimitSeconds*1000)
	if err != nil {
		return nil, fmt.Errorf("query last passes: %w", err)
	}
	defer rows.Close()

	passes := make(map[string]ranking.LastPass)
	for rows.Next() {
		var memberID string
		var pass ranking.LastPass
		var sort sql.NullInt64
		var name sql.NullString
		if err := rows.Scan(&memberID, &pass.TimeMs, &sort, &name); err != nil {
			return nil, fmt.Errorf("scan last pass: %w", err)
		}
		pass.CheckpointSort = nullableInt64(sort)
		if name.Valid {
			n := name.String
			pass.CheckpointName = &n
		}
		passes[memberID] = pass
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate last passes: %w", err)
	}

	return passes, nil
}
