package sqlite

import (
	"context"
	"fmt"
)

// ListReservedMemberIDs reads packet-reserve state in bulk. Reserve registrations
// are not participants expected at finish; absent legacy packet metadata means
// an ordinary registration. A deleted projection must not reserve a live member.
func (s *Store) ListReservedMemberIDs(ctx context.Context, eventID string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT registration_id FROM packet_issuance_registrations
		WHERE event_id=? AND deleted=0 AND COALESCE(json_extract(value_json,'$.reserve'),0)=1`, eventID)
	if err != nil {
		return nil, fmt.Errorf("list reserve registrations: %w", err)
	}
	defer rows.Close()
	reserved := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		reserved[id] = true
	}
	return reserved, rows.Err()
}
