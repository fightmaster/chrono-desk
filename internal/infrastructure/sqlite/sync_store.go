package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

type ObservationBatch struct {
	BatchID          string
	OriginInstanceID string
	FirstSequence    int64
	LastSequence     int64
	Items            []domain.RfidLog
}

type ObservationOutboxAck struct {
	ObservationID  string
	OriginSequence int64
	Status         string
	Reason         string
}

// SyncConfig is the per-event run5 sync target (base URL + token) plus the last
// push result. One row per event database.
type SyncConfig struct {
	BaseURL         string  `json:"base_url"`
	Token           string  `json:"token"`
	LastSyncedAt    *int64  `json:"last_synced_at"`
	LastPayloadHash *string `json:"last_payload_hash"`
}

// GetSyncConfig returns the event's sync target; a zero value if unset.
func (s *Store) GetSyncConfig(ctx context.Context, eventID string) (SyncConfig, error) {
	var c SyncConfig
	err := s.db.QueryRowContext(ctx,
		`SELECT base_url, token, last_synced_at, last_payload_hash FROM sync_config WHERE event_id = ?`,
		eventID).Scan(&c.BaseURL, &c.Token, &c.LastSyncedAt, &c.LastPayloadHash)
	if errors.Is(err, sql.ErrNoRows) {
		return SyncConfig{}, nil
	}
	if err != nil {
		return SyncConfig{}, fmt.Errorf("get sync config %s: %w", eventID, err)
	}
	return c, nil
}

// SetSyncConfig upserts the base URL and token, preserving the last-sync fields.
func (s *Store) SetSyncConfig(ctx context.Context, eventID, baseURL, token string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sync_config (event_id, base_url, token) VALUES (?, ?, ?)
		ON CONFLICT(event_id) DO UPDATE SET base_url=excluded.base_url, token=excluded.token`,
		eventID, baseURL, token)
	if err != nil {
		return fmt.Errorf("set sync config %s: %w", eventID, err)
	}
	return nil
}

// SetSyncResult records a successful push (timestamp + payload hash) so a
// re-push of identical data can be skipped.
func (s *Store) SetSyncResult(ctx context.Context, eventID string, whenMs int64, payloadHash string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sync_config (event_id, last_synced_at, last_payload_hash) VALUES (?, ?, ?)
		ON CONFLICT(event_id) DO UPDATE SET last_synced_at=excluded.last_synced_at, last_payload_hash=excluded.last_payload_hash`,
		eventID, whenMs, payloadHash)
	if err != nil {
		return fmt.Errorf("set sync result %s: %w", eventID, err)
	}
	return nil
}

// PrepareObservationBatch returns the outstanding sent batch unchanged on
// retry, otherwise atomically assigns up to limit pending rows to a stable
// batch. Imported observations have no outbox row and can never appear here.
func (s *Store) PrepareObservationBatch(ctx context.Context, eventID string, limit int, attemptedAt time.Time) (*ObservationBatch, error) {
	if limit <= 0 || limit > 20_000 {
		return nil, fmt.Errorf("observation batch limit must be between 1 and 20000")
	}
	tx, err := s.root.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin observation batch: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	var batchID sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT o.batch_id
		FROM observation_outbox o
		JOIN rfid_logs l ON l.id = o.observation_id
		WHERE l.event_id = ? AND o.state = 'sent' AND o.batch_id IS NOT NULL
		ORDER BY o.origin_sequence LIMIT 1`, eventID).Scan(&batchID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("find sent observation batch: %w", err)
	}
	if batchID.Valid {
		if _, err := tx.ExecContext(ctx, `
			UPDATE observation_outbox
			SET attempt_count = attempt_count + 1, last_attempt_at = ?
			WHERE batch_id = ? AND state = 'sent'`, attemptedAt.UnixMilli(), batchID.String); err != nil {
			return nil, fmt.Errorf("mark observation batch retry: %w", err)
		}
		batch, err := loadObservationBatch(ctx, tx, eventID, batchID.String)
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit observation batch retry: %w", err)
		}
		return batch, nil
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT o.origin_sequence
		FROM observation_outbox o
		JOIN rfid_logs l ON l.id = o.observation_id
		WHERE l.event_id = ? AND o.state = 'pending'
		ORDER BY o.origin_sequence LIMIT ?`, eventID, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending observations: %w", err)
	}
	var sequences []int64
	for rows.Next() {
		var sequence int64
		if err := rows.Scan(&sequence); err != nil {
			rows.Close()
			return nil, err
		}
		sequences = append(sequences, sequence)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(sequences) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit empty observation batch: %w", err)
		}
		return nil, nil
	}
	batchIDValue := fmt.Sprintf("%s:%d-%d", s.originInstanceID, sequences[0], sequences[len(sequences)-1])
	for _, chunk := range int64Chunks(sequences, 400) {
		args := make([]any, 0, len(chunk)+3)
		args = append(args, batchIDValue, attemptedAt.UnixMilli())
		for _, sequence := range chunk {
			args = append(args, sequence)
		}
		query := `UPDATE observation_outbox
			SET state = 'sent', batch_id = ?, attempt_count = attempt_count + 1, last_attempt_at = ?
			WHERE state = 'pending' AND origin_sequence IN (` + placeholders(len(chunk)) + `)`
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return nil, fmt.Errorf("assign observation batch: %w", err)
		}
	}
	batch, err := loadObservationBatch(ctx, tx, eventID, batchIDValue)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit observation batch: %w", err)
	}
	return batch, nil
}

func loadObservationBatch(ctx context.Context, db database, eventID, batchID string) (*ObservationBatch, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT l.id, l.event_id, l.status, l.number, l.time_ms, l.ant, l.epc, l.rssi, l.board, l.disabled_at,
			l.observation_version, l.capture_source_id, l.origin_system, l.origin_instance_id, l.origin_sequence
		FROM observation_outbox o
		JOIN rfid_logs l ON l.id = o.observation_id
		WHERE l.event_id = ? AND o.batch_id = ? AND o.state = 'sent'
		ORDER BY o.origin_sequence`, eventID, batchID)
	if err != nil {
		return nil, fmt.Errorf("load observation batch: %w", err)
	}
	defer rows.Close()
	batch := &ObservationBatch{BatchID: batchID}
	for rows.Next() {
		var item domain.RfidLog
		if err := rows.Scan(
			&item.ID, &item.EventID, &item.Status, &item.Number, &item.TimeMs, &item.Ant, &item.EPC, &item.RSSI,
			&item.Board, &item.DisabledAt, &item.ObservationVersion, &item.CaptureSourceID, &item.OriginSystem,
			&item.OriginInstanceID, &item.OriginSequence,
		); err != nil {
			return nil, fmt.Errorf("scan observation batch: %w", err)
		}
		if len(batch.Items) == 0 {
			batch.OriginInstanceID = item.OriginInstanceID
			batch.FirstSequence = item.OriginSequence
		}
		if item.OriginInstanceID != batch.OriginInstanceID {
			return nil, fmt.Errorf("observation batch contains mixed origin instances")
		}
		batch.LastSequence = item.OriginSequence
		batch.Items = append(batch.Items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(batch.Items) == 0 {
		return nil, fmt.Errorf("observation batch %s is empty", batchID)
	}
	return batch, nil
}

// ApplyObservationAck transitions every sent row in a batch atomically. A
// malformed or partial acknowledgement leaves the whole batch sent for retry.
func (s *Store) ApplyObservationAck(ctx context.Context, batchID string, acknowledgements []ObservationOutboxAck, ackedAt time.Time) error {
	return s.WithinTx(ctx, func(txStore *Store) error {
		rows, err := txStore.db.QueryContext(ctx, `
			SELECT observation_id, origin_sequence
			FROM observation_outbox WHERE batch_id = ? AND state = 'sent'
			ORDER BY origin_sequence`, batchID)
		if err != nil {
			return fmt.Errorf("list sent acknowledgement rows: %w", err)
		}
		expected := map[int64]string{}
		for rows.Next() {
			var id string
			var sequence int64
			if err := rows.Scan(&id, &sequence); err != nil {
				rows.Close()
				return err
			}
			expected[sequence] = id
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(expected) == 0 || len(expected) != len(acknowledgements) {
			return fmt.Errorf("acknowledgement does not cover sent batch")
		}
		accepted := make([]int64, 0, len(acknowledgements))
		rejected := map[int64]string{}
		seen := map[int64]bool{}
		for _, ack := range acknowledgements {
			if seen[ack.OriginSequence] || expected[ack.OriginSequence] != ack.ObservationID {
				return fmt.Errorf("acknowledgement identity mismatch")
			}
			seen[ack.OriginSequence] = true
			switch ack.Status {
			case "inserted", "duplicate":
				accepted = append(accepted, ack.OriginSequence)
			case "rejected":
				rejected[ack.OriginSequence] = ack.Reason
			default:
				return fmt.Errorf("unknown acknowledgement status %q", ack.Status)
			}
		}
		for _, chunk := range int64Chunks(accepted, 400) {
			args := make([]any, 0, len(chunk)+2)
			args = append(args, ackedAt.UnixMilli())
			for _, sequence := range chunk {
				args = append(args, sequence)
			}
			query := `UPDATE observation_outbox SET state = 'acked', acked_at = ?, rejection = NULL
				WHERE state = 'sent' AND origin_sequence IN (` + placeholders(len(chunk)) + `)`
			if _, err := txStore.db.ExecContext(ctx, query, args...); err != nil {
				return fmt.Errorf("acknowledge observation batch: %w", err)
			}
		}
		for sequence, reason := range rejected {
			if _, err := txStore.db.ExecContext(ctx, `
				UPDATE observation_outbox SET state = 'rejected', rejection = ?
				WHERE state = 'sent' AND origin_sequence = ?`, reason, sequence); err != nil {
				return fmt.Errorf("reject observation: %w", err)
			}
		}
		return nil
	})
}

func int64Chunks(values []int64, size int) [][]int64 {
	var chunks [][]int64
	for len(values) > 0 {
		n := min(size, len(values))
		chunks = append(chunks, values[:n])
		values = values[n:]
	}
	return chunks
}

func placeholders(count int) string {
	return strings.TrimRight(strings.Repeat("?,", count), ",")
}
