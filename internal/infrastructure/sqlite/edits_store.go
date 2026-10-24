package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// LocalChange is one journal entry; values are JSON-encoded.
type LocalChange struct {
	ClubRelayOperationID  *string         `json:"club_relay_operation_id,omitempty"`
	ClubRelayAcknowledged bool            `json:"club_relay_acknowledged,omitempty"`
	ClubPersonID          json.RawMessage `json:"club_person_id,omitempty"`
	ID                    int64           `json:"id"`
	Entity                string          `json:"entity"`
	EntityID              string          `json:"entity_id"`
	Field                 string          `json:"field"`
	OldValue              string          `json:"old_value"`
	NewValue              string          `json:"new_value"`
	ChangedAt             int64           `json:"changed_at"`
}

// UpdateEntityField writes one whitelisted field (the caller validates the
// table/field against the edit whitelist) and returns the previous value as
// driver-typed any. value must be nil, int64 or string.
func (s *Store) UpdateEntityField(ctx context.Context, table, field, id string, value any) (old any, err error) {
	if table == "members" && field == "club" {
		return s.updateMemberClubField(ctx, id, value)
	}
	defer func() {
		if err == nil && (table == "members" || table == "races" || table == "categories") {
			s.finishChanged()
		}
	}()
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
	if table == "checkpoints" {
		cp, err := s.GetCheckpoint(ctx, id)
		if err != nil {
			return nil, err
		}
		if err := validateCheckpointRowWindow(cp); err != nil {
			return nil, err
		}
	}
	return old, nil
}

func (s *Store) InsertLocalChange(ctx context.Context, c LocalChange) error {
	if c.Entity == "member" && c.Field == "club" {
		return s.WithinTx(ctx, func(tx *Store) error {
			binding, err := tx.ClubOwnerBinding(ctx, c.EntityID)
			if err != nil {
				return err
			}
			return tx.insertLocalChange(ctx, c, binding)
		})
	}
	return s.insertLocalChange(ctx, c, nil)
}

func (s *Store) insertLocalChange(ctx context.Context, c LocalChange, binding json.RawMessage) error {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO local_changes (entity, entity_id, field, old_value, new_value, changed_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		c.Entity, c.EntityID, c.Field, c.OldValue, c.NewValue, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("journal change: %w", err)
	}
	if len(binding) > 0 {
		id, err := result.LastInsertId()
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO club_change_owners(change_id,person_json) VALUES(?,?)`, id, string(binding))
		return err
	}
	return nil
}

// ListLocalChanges returns the journal, oldest first (replay order).
func (s *Store) ListLocalChanges(ctx context.Context) ([]LocalChange, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.id, l.entity, l.entity_id, l.field, l.old_value, l.new_value, l.changed_at,o.person_json,c.operation_id,COALESCE(c.site_acknowledged,0)
 FROM local_changes l LEFT JOIN club_change_owners o ON o.change_id=l.id LEFT JOIN packet_club_outbox c ON c.change_id=l.id ORDER BY l.id`)
	if err != nil {
		return nil, fmt.Errorf("list changes: %w", err)
	}
	defer rows.Close()

	var changes []LocalChange
	for rows.Next() {
		var c LocalChange
		var owner *string
		if err := rows.Scan(&c.ID, &c.Entity, &c.EntityID, &c.Field, &c.OldValue, &c.NewValue, &c.ChangedAt, &owner, &c.ClubRelayOperationID, &c.ClubRelayAcknowledged); err != nil {
			return nil, err
		}
		if owner != nil {
			c.ClubPersonID = json.RawMessage(*owner)
		}
		changes = append(changes, c)
	}
	return changes, rows.Err()
}
