package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

type PacketClubOutbound struct {
	Sequence                      int64
	OperationID, EventID, ScopeID string
	OperationJSON                 []byte
	Heads                         []string
	ChangeID                      int64
	NextAttemptAt                 int64
	Attempts                      int
}

type PacketClubDeliveryCounts struct {
	Pending int `json:"pending"`
	Review  int `json:"review"`
}

func addPacketClubOutbox(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS packet_club_outbox(
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,operation_id TEXT NOT NULL UNIQUE,
 event_id TEXT NOT NULL,scope_id TEXT NOT NULL,operation_json BLOB NOT NULL,heads_json TEXT NOT NULL,
 change_id INTEGER NOT NULL UNIQUE,site_acknowledged INTEGER NOT NULL DEFAULT 0,
 site_outcome TEXT,site_code TEXT,attempts INTEGER NOT NULL DEFAULT 0,next_attempt_at INTEGER NOT NULL DEFAULT 0);
 CREATE INDEX IF NOT EXISTS packet_club_outbox_pending ON packet_club_outbox(event_id,site_acknowledged,sequence);`)
	return err
}
func (s *Store) ClubOriginInstanceID() string { return s.originInstanceID }

func (s *Store) EnqueuePacketClubEdit(ctx context.Context, eventID, scopeID, memberID, operationID string, body []byte, heads []string) error {
	var changeID int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM local_changes WHERE entity='member' AND entity_id=? AND field='club' ORDER BY id DESC LIMIT 1`, memberID).Scan(&changeID); err != nil {
		return err
	}
	encoded, err := json.Marshal(heads)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO packet_club_outbox(operation_id,event_id,scope_id,operation_json,heads_json,change_id) VALUES(?,?,?,?,?,?)`, operationID, eventID, scopeID, body, string(encoded), changeID)
	return err
}
func (s *Store) PendingPacketClubEdits(ctx context.Context, eventID string) ([]PacketClubOutbound, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT sequence,operation_id,event_id,scope_id,operation_json,heads_json,change_id,next_attempt_at,attempts FROM packet_club_outbox WHERE event_id=? AND site_acknowledged=0 ORDER BY sequence LIMIT 64`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []PacketClubOutbound{}
	for rows.Next() {
		var row PacketClubOutbound
		var heads string
		if err := rows.Scan(&row.Sequence, &row.OperationID, &row.EventID, &row.ScopeID, &row.OperationJSON, &heads, &row.ChangeID, &row.NextAttemptAt, &row.Attempts); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(heads), &row.Heads); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// Imported/native profile heads need no packet operation ACK. Local canonical
// operations must be accepted before dependent metadata can be forwarded.
func (s *Store) PacketClubDependencies(ctx context.Context, row PacketClubOutbound) (ready, conflicted bool, err error) {
	for _, head := range row.Heads {
		var acknowledged bool
		var outcome *string
		err = s.db.QueryRowContext(ctx, `SELECT site_acknowledged,site_outcome FROM packet_issuance_operations WHERE event_id=? AND operation_id=?`, row.EventID, head).Scan(&acknowledged, &outcome)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return false, false, err
		}
		if !acknowledged {
			return false, false, nil
		}
		if outcome != nil && *outcome != "applied" && *outcome != "equivalent" {
			return false, true, nil
		}
	}
	return true, false, nil
}
func (s *Store) AcknowledgePacketClubEdit(ctx context.Context, operationID, outcome string, code *string) error {
	if outcome != "applied" && outcome != "conflict" && outcome != "rejected" {
		return fmt.Errorf("invalid club receipt outcome")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE packet_club_outbox SET site_acknowledged=1,site_outcome=?,site_code=?,attempts=attempts+1,next_attempt_at=0 WHERE operation_id=?`, outcome, code, operationID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("club receipt identity mismatch")
	}
	return nil
}
func (s *Store) DeferPacketClubEdit(ctx context.Context, row PacketClubOutbound, code string, review bool, when time.Time) error {
	delays := []time.Duration{time.Second, 4 * time.Second, 15 * time.Second, time.Minute}
	index := row.Attempts
	if index > 3 {
		index = 3
	}
	var outcome *string
	if review {
		value := "review"
		outcome = &value
	}
	result, err := s.db.ExecContext(ctx, `UPDATE packet_club_outbox SET site_acknowledged=?,site_outcome=?,site_code=?,attempts=attempts+1,next_attempt_at=? WHERE operation_id=?`, review, outcome, code, when.Add(delays[index]).UnixMilli(), row.OperationID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("club delivery identity mismatch")
	}
	return nil
}
func (s *Store) PacketClubDeliveryCounts(ctx context.Context, eventID string) (PacketClubDeliveryCounts, error) {
	var result PacketClubDeliveryCounts
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(site_acknowledged=0),0),COALESCE(SUM(site_outcome IN('conflict','rejected','review')),0) FROM packet_club_outbox WHERE event_id=?`, eventID).Scan(&result.Pending, &result.Review)
	return result, err
}

// An authoritative snapshot cannot overwrite a newer unacknowledged local edit
// to this same person/registration while its HTTP response was in flight.
func (s *Store) protectPendingPacketClubs(ctx context.Context, eventID string, clubs map[string]*string, owners map[string]*string) error {
	rows, err := s.db.QueryContext(ctx, `SELECT operation_json FROM packet_club_outbox WHERE event_id=? AND site_acknowledged=0`, eventID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var body []byte
		if err = rows.Scan(&body); err != nil {
			return err
		}
		var operation struct {
			RegistrationID string  `json:"registrationId"`
			PersonID       *string `json:"personId"`
		}
		if err = json.Unmarshal(body, &operation); err != nil {
			return err
		}
		if owner, provided := owners[operation.RegistrationID]; provided && (owner == nil && operation.PersonID == nil || owner != nil && operation.PersonID != nil && *owner == *operation.PersonID) {
			delete(clubs, operation.RegistrationID)
		}
	}
	return rows.Err()
}
