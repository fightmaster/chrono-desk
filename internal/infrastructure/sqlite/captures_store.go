package sqlite

import (
	"context"
	"fmt"
	"time"
)

// pending_captures: wall-clock «Зафиксировать время» finishes with no member
// yet. Persisted so they survive a restart; binding a number elsewhere turns
// one into a manual result and deletes the capture.

// PendingCapture is one unbound wall-clock finish.
type PendingCapture struct {
	ID     int64 `json:"id"`
	TimeMs int64 `json:"time_ms"`
}

// CreatePendingCapture stores a wall-clock capture and returns its id.
func (s *Store) CreatePendingCapture(ctx context.Context, eventID string, timeMs int64) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO pending_captures (event_id, time_ms, created_at) VALUES (?, ?, ?)`,
		eventID, timeMs, time.Now().UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("create pending capture: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create pending capture id: %w", err)
	}
	return id, nil
}

// ListPendingCaptures returns the event's unbound captures, newest first (the
// live feed prepends them).
func (s *Store) ListPendingCaptures(ctx context.Context, eventID string) ([]PendingCapture, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, time_ms FROM pending_captures WHERE event_id = ? ORDER BY id DESC`, eventID)
	if err != nil {
		return nil, fmt.Errorf("list pending captures: %w", err)
	}
	defer rows.Close()

	captures := make([]PendingCapture, 0)
	for rows.Next() {
		var c PendingCapture
		if err := rows.Scan(&c.ID, &c.TimeMs); err != nil {
			return nil, err
		}
		captures = append(captures, c)
	}
	return captures, rows.Err()
}

// DeletePendingCapture removes a capture (bound to a member, or discarded).
func (s *Store) DeletePendingCapture(ctx context.Context, eventID string, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM pending_captures WHERE event_id = ? AND id = ?`, eventID, id)
	if err != nil {
		return fmt.Errorf("delete pending capture: %w", err)
	}
	return nil
}
