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
	return s.listMemberPassRows(ctx, memberID, `
		SELECT l.id, l.time_ms, l.board, l.ant, l.rssi, l.disabled_at,
		       c.id, c.name, c.sort
		FROM rfid_logs l
		LEFT JOIN results r ON r.rfid_log_id = l.id AND r.member_id = ?
		LEFT JOIN checkpoints c ON c.id = r.checkpoint_id
		WHERE l.epc = ? AND l.event_id = ?
		ORDER BY l.time_ms, l.id`, memberID, epc, eventID)
}

// ListAcceptedMemberPasses starts from the member's derived results, avoiding
// scanning the raw event history when the drawer only needs accepted passes.
func (s *Store) ListAcceptedMemberPasses(ctx context.Context, eventID, memberID, epc string) ([]MemberPassRow, error) {
	return s.listMemberPassRows(ctx, memberID, acceptedMemberPassQuery, memberID, eventID, epc)
}

// CROSS JOIN deliberately keeps the small member-results lookup before raw
// history even on event files without ANALYZE statistics.
const acceptedMemberPassQuery = `
		SELECT l.id, l.time_ms, l.board, l.ant, l.rssi, l.disabled_at,
		       c.id, c.name, c.sort
		FROM results r
		CROSS JOIN rfid_logs l ON l.id = r.rfid_log_id
		JOIN checkpoints c ON c.id = r.checkpoint_id
		WHERE r.member_id = ? AND l.event_id = ? AND l.epc = ?
		  AND l.disabled_at IS NULL
		ORDER BY l.time_ms, l.id`

func (s *Store) listMemberPassRows(ctx context.Context, memberID, query string, args ...any) ([]MemberPassRow, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
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
