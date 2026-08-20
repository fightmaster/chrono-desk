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
	return s.WithinTx(ctx, func(txStore *Store) error {
		var err error
		if raceID == "" {
			_, err = txStore.db.ExecContext(ctx,
				`DELETE FROM results WHERE event_id = ? AND rfid_log_id IS NOT NULL`, eventID)
			if err == nil {
				_, err = txStore.db.ExecContext(ctx,
					`UPDATE members SET finish_time_ms = NULL, clean_time = NULL WHERE event_id = ?`, eventID)
			}
		} else {
			_, err = txStore.db.ExecContext(ctx,
				`DELETE FROM results WHERE event_id = ? AND race_id = ? AND rfid_log_id IS NOT NULL`, eventID, raceID)
			if err == nil {
				_, err = txStore.db.ExecContext(ctx,
					`UPDATE members SET finish_time_ms = NULL, clean_time = NULL WHERE event_id = ? AND race_id = ?`, eventID, raceID)
			}
		}
		if err != nil {
			return fmt.Errorf("wipe derived results: %w", err)
		}
		return nil
	})
}

// ListRfidLogs returns the event's logs in replay order (time, then id for a
// deterministic tie-break). Disabled logs are included — the engine skips
// them itself, keeping the disable check in one place.
func (s *Store) ListRfidLogs(ctx context.Context, eventID string) ([]domain.RfidLog, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, event_id, status, number, time_ms, ant, epc, rssi, board, disabled_at,
			observation_version, capture_source_id, origin_system, origin_instance_id, origin_sequence
		FROM rfid_logs WHERE event_id = ?
		ORDER BY time_ms ASC, id ASC`, eventID)
	if err != nil {
		return nil, fmt.Errorf("list rfid logs: %w", err)
	}
	defer rows.Close()

	var logs []domain.RfidLog
	for rows.Next() {
		var l domain.RfidLog
		var disabledAt, observationVersion, originSequence sql.NullInt64
		var captureSourceID, originSystem, originInstanceID sql.NullString
		if err := rows.Scan(
			&l.ID, &l.EventID, &l.Status, &l.Number, &l.TimeMs, &l.Ant, &l.EPC, &l.RSSI, &l.Board, &disabledAt,
			&observationVersion, &captureSourceID, &originSystem, &originInstanceID, &originSequence,
		); err != nil {
			return nil, err
		}
		l.DisabledAt = nullableInt64(disabledAt)
		if observationVersion.Valid {
			l.ObservationVersion = int(observationVersion.Int64)
		}
		if captureSourceID.Valid {
			l.CaptureSourceID = captureSourceID.String
		}
		if originSystem.Valid {
			l.OriginSystem = originSystem.String
		}
		if originInstanceID.Valid {
			l.OriginInstanceID = originInstanceID.String
		}
		if originSequence.Valid {
			l.OriginSequence = originSequence.Int64
		}
		logs = append(logs, l)
	}
	return logs, rows.Err()
}
