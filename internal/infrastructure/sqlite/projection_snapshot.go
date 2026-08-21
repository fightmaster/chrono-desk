package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
)

// ProjectionEvidence identifies the exact configuration and input snapshot
// used to build a replay plan. It is recalculated inside the execution
// transaction; a mismatch fails closed to a full event replay.
type ProjectionEvidence struct {
	ConfigVersion  string
	InputWatermark string
}

func (s *Store) ProjectionEvidence(ctx context.Context, eventID string) (ProjectionEvidence, error) {
	var evidence ProjectionEvidence
	err := s.WithinTx(ctx, func(txStore *Store) error {
		var err error
		evidence, err = txStore.projectionEvidence(ctx, eventID)
		return err
	})
	return evidence, err
}

func (s *Store) projectionEvidence(ctx context.Context, eventID string) (ProjectionEvidence, error) {
	config := sha256.New()
	for _, query := range []struct {
		label string
		sql   string
		args  []any
	}{
		{"event", `SELECT id, name, slug, date, timezone, use_race_date_for_age FROM events WHERE id = ? ORDER BY id`, []any{eventID}},
		{"races", `SELECT id, event_id, date, started_at_ms, format, time_limit_seconds, category_excludes_top_by_gender FROM races WHERE event_id = ? ORDER BY id`, []any{eventID}},
		{"categories", `SELECT c.id, c.name, c.min, c.max, c.gender
			FROM categories c
			WHERE EXISTS (
				SELECT 1 FROM race_categories rc JOIN races r ON r.id = rc.race_id
				WHERE rc.category_id = c.id AND r.event_id = ?
			) OR EXISTS (
				SELECT 1 FROM members m WHERE m.category_id = c.id AND m.event_id = ?
			)
			ORDER BY c.id`, []any{eventID, eventID}},
		{"race_categories", `SELECT rc.race_id, rc.category_id
			FROM race_categories rc JOIN races r ON r.id = rc.race_id
			WHERE r.event_id = ? ORDER BY rc.race_id, rc.category_id`, []any{eventID}},
		{"checkpoints", `SELECT id, event_id, race_id, type, sort, board, since_ms, since_offset_seconds, sleep_after_prev_seconds FROM checkpoints WHERE event_id = ? ORDER BY id`, []any{eventID}},
		{"members", `SELECT id, event_id, race_id, category_id, number, epc, gender, dob, status, start_time_ms, finish_time_ms
			FROM members WHERE event_id = ? ORDER BY id`, []any{eventID}},
	} {
		if err := hashQuery(ctx, s.db, config, query.label, query.sql, query.args...); err != nil {
			return ProjectionEvidence{}, err
		}
	}

	input := sha256.New()
	if err := hashQuery(ctx, s.db, input, "rfid_logs", `
		SELECT id, event_id, status, number, time_ms, ant, epc, rssi, board, disabled_at,
			observation_version, capture_source_id, origin_system, origin_instance_id, origin_sequence
		FROM rfid_logs WHERE event_id = ? ORDER BY id`, eventID); err != nil {
		return ProjectionEvidence{}, err
	}
	if err := hashQuery(ctx, s.db, input, "manual_results", `
		SELECT id, event_id, race_id, member_id, time_ms, number
		FROM results WHERE event_id = ? AND rfid_log_id IS NULL AND checkpoint_id IS NULL
		ORDER BY id`, eventID); err != nil {
		return ProjectionEvidence{}, err
	}
	var cursor *string
	if err := s.db.QueryRowContext(ctx, `SELECT (SELECT pull_cursor FROM sync_config WHERE event_id = ?)`, eventID).Scan(&cursor); err != nil {
		return ProjectionEvidence{}, fmt.Errorf("read projection cursor: %w", err)
	}
	encodedCursor, _ := json.Marshal(cursor)
	_, _ = input.Write(encodedCursor)

	return ProjectionEvidence{
		ConfigVersion:  "chrono-desk-config-sha256:" + hex.EncodeToString(config.Sum(nil)),
		InputWatermark: "chrono-desk-input-sha256:" + hex.EncodeToString(input.Sum(nil)),
	}, nil
}

func hashQuery(ctx context.Context, db database, target hash.Hash, label, query string, args ...any) error {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("projection snapshot %s: %w", label, err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return fmt.Errorf("projection snapshot %s columns: %w", label, err)
	}
	_, _ = target.Write([]byte(label + "\n"))
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return fmt.Errorf("projection snapshot %s scan: %w", label, err)
		}
		encoded, err := json.Marshal(values)
		if err != nil {
			return fmt.Errorf("projection snapshot %s encode: %w", label, err)
		}
		_, _ = target.Write(encoded)
		_, _ = target.Write([]byte{'\n'})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("projection snapshot %s rows: %w", label, err)
	}
	return nil
}

type ObservationMemberMatch struct {
	MemberID  string
	RaceID    string
	Found     bool
	Ambiguous bool
}

// ResolveObservationMember fails closed when a number/EPC identifies more
// than one participant. Choosing LIMIT 1 would make targeted replay depend on
// SQLite row order and could modify the wrong race.
func (s *Store) ResolveObservationMember(ctx context.Context, eventID string, number int64, epc string) (ObservationMemberMatch, error) {
	query := `SELECT id, race_id FROM members WHERE event_id = ? AND epc = ? ORDER BY id LIMIT 2`
	identity := any(epc)
	if number > 0 {
		query = `SELECT id, race_id FROM members WHERE event_id = ? AND number = ? ORDER BY id LIMIT 2`
		identity = number
	}
	rows, err := s.db.QueryContext(ctx, query, eventID, identity)
	if err != nil {
		return ObservationMemberMatch{}, fmt.Errorf("resolve observation member: %w", err)
	}
	defer rows.Close()
	var matches []ObservationMemberMatch
	for rows.Next() {
		var match ObservationMemberMatch
		if err := rows.Scan(&match.MemberID, &match.RaceID); err != nil {
			return ObservationMemberMatch{}, err
		}
		matches = append(matches, match)
	}
	if err := rows.Err(); err != nil {
		return ObservationMemberMatch{}, err
	}
	if len(matches) == 0 {
		return ObservationMemberMatch{}, nil
	}
	if len(matches) > 1 {
		return ObservationMemberMatch{Ambiguous: true}, nil
	}
	matches[0].Found = true
	return matches[0], nil
}
