package sqlite

import (
	"context"
	"fmt"
)

// MemberPassRow is one RFID read and the checkpoint result it produced, if any.
type MemberPassRow struct {
	LogID          string
	TimeMs         int64
	Board          string
	Ant            int
	RSSI           int
	DisabledAt     *int64
	CheckpointID   *string
	CheckpointName *string
	CheckpointSort *int64
}

// ListMemberPasses returns the tagged reads associated with a participant.
func (s *Store) ListMemberPasses(ctx context.Context, eventID, memberID, epc string) ([]MemberPassRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.id, l.time_ms, l.board, l.ant, l.rssi, l.disabled_at,
		       c.id, c.name, c.sort
		FROM rfid_logs l
		LEFT JOIN results r ON r.rfid_log_id = l.id AND r.member_id = ?
		LEFT JOIN checkpoints c ON c.id = r.checkpoint_id
		WHERE l.epc = ? AND l.event_id = ?
		ORDER BY l.time_ms, l.id`, memberID, epc, eventID)
	if err != nil {
		return nil, fmt.Errorf("passes for %s: %w", memberID, err)
	}
	defer rows.Close()

	out := []MemberPassRow{}
	for rows.Next() {
		var p MemberPassRow
		if err := rows.Scan(&p.LogID, &p.TimeMs, &p.Board, &p.Ant, &p.RSSI, &p.DisabledAt,
			&p.CheckpointID, &p.CheckpointName, &p.CheckpointSort); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
