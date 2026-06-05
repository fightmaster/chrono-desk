package sqlite

import (
	"context"
	"fmt"
	"time"
)

// LocalChange is one journal entry; values are JSON-encoded.
type LocalChange struct {
	ID        int64  `json:"id"`
	Entity    string `json:"entity"`
	EntityID  string `json:"entity_id"`
	Field     string `json:"field"`
	OldValue  string `json:"old_value"`
	NewValue  string `json:"new_value"`
	ChangedAt int64  `json:"changed_at"`
}

// UpdateEntityField writes one whitelisted field (the caller validates the
// table/field against the edit whitelist) and returns the previous value as
// driver-typed any. value must be nil, int64 or string.
func (s *Store) UpdateEntityField(ctx context.Context, table, field, id string, value any) (old any, err error) {
	// table/field come from a compile-time whitelist, never from user input —
	// safe to interpolate.
	row := s.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT %s FROM %s WHERE id = ?`, field, table), id)
	if err := row.Scan(&old); err != nil {
		return nil, fmt.Errorf("read %s.%s for %s: %w", table, field, id, err)
	}

	res, err := s.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE %s SET %s = ? WHERE id = ?`, table, field), value, id)
	if err != nil {
		return nil, fmt.Errorf("update %s.%s for %s: %w", table, field, id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("%s %s not found", table, id)
	}
	return old, nil
}

func (s *Store) InsertLocalChange(ctx context.Context, c LocalChange) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO local_changes (entity, entity_id, field, old_value, new_value, changed_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		c.Entity, c.EntityID, c.Field, c.OldValue, c.NewValue, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("journal change: %w", err)
	}
	return nil
}

// ListLocalChanges returns the journal, oldest first (replay order).
func (s *Store) ListLocalChanges(ctx context.Context) ([]LocalChange, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, entity, entity_id, field, old_value, new_value, changed_at
		FROM local_changes ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list changes: %w", err)
	}
	defer rows.Close()

	var changes []LocalChange
	for rows.Next() {
		var c LocalChange
		if err := rows.Scan(&c.ID, &c.Entity, &c.EntityID, &c.Field, &c.OldValue, &c.NewValue, &c.ChangedAt); err != nil {
			return nil, err
		}
		changes = append(changes, c)
	}
	return changes, rows.Err()
}
