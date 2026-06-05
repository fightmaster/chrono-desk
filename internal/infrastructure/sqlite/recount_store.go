package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

// WipeDerivedResults removes rfid-derived results and resets member finish
// times before a replay. Mirrors run5's RecountRfid wipe: manual results
// (rfid_log_id IS NULL) and member start times survive — start is re-derived
// or was set deliberately on the site.
func (s *Store) WipeDerivedResults(ctx context.Context, eventID, raceID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin wipe: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	if raceID == "" {
		_, err = tx.ExecContext(ctx,
			`DELETE FROM results WHERE event_id = ? AND rfid_log_id IS NOT NULL`, eventID)
		if err == nil {
			_, err = tx.ExecContext(ctx,
				`UPDATE members SET finish_time_ms = NULL, clean_time = NULL WHERE event_id = ?`, eventID)
		}
	} else {
		_, err = tx.ExecContext(ctx,
			`DELETE FROM results WHERE event_id = ? AND race_id = ? AND rfid_log_id IS NOT NULL`, eventID, raceID)
		if err == nil {
			_, err = tx.ExecContext(ctx,
				`UPDATE members SET finish_time_ms = NULL, clean_time = NULL WHERE event_id = ? AND race_id = ?`, eventID, raceID)
		}
	}
	if err != nil {
		return fmt.Errorf("wipe derived results: %w", err)
	}
	return tx.Commit()
}

// ListRfidLogs returns the event's logs in replay order (time, then id for a
// deterministic tie-break). Disabled logs are included — the engine skips
// them itself, keeping the disable check in one place.
func (s *Store) ListRfidLogs(ctx context.Context, eventID string) ([]domain.RfidLog, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, event_id, status, number, time_ms, ant, epc, rssi, board, disabled_at
		FROM rfid_logs WHERE event_id = ?
		ORDER BY time_ms ASC, id ASC`, eventID)
	if err != nil {
		return nil, fmt.Errorf("list rfid logs: %w", err)
	}
	defer rows.Close()

	var logs []domain.RfidLog
	for rows.Next() {
		var l domain.RfidLog
		var disabledAt sql.NullInt64
		if err := rows.Scan(&l.ID, &l.EventID, &l.Status, &l.Number, &l.TimeMs, &l.Ant, &l.EPC, &l.RSSI, &l.Board, &disabledAt); err != nil {
			return nil, err
		}
		l.DisabledAt = nullableInt64(disabledAt)
		logs = append(logs, l)
	}
	return logs, rows.Err()
}
