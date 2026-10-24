package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

// Club is display metadata, not Team or canonical packet Person state.
func addMemberClubMetadata(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS member_club_metadata (
  event_id TEXT NOT NULL,member_id TEXT NOT NULL REFERENCES members(id) ON DELETE CASCADE,
  club TEXT,PRIMARY KEY(event_id,member_id));
 CREATE TABLE IF NOT EXISTS club_change_owners(change_id INTEGER PRIMARY KEY REFERENCES local_changes(id) ON DELETE CASCADE,person_json TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS packet_club_edit_receipts(operation_id TEXT PRIMARY KEY,connection_id TEXT NOT NULL,event_id TEXT NOT NULL,scope_id TEXT NOT NULL,origin_instance_id TEXT NOT NULL,content_hash TEXT NOT NULL,operation_json BLOB NOT NULL,receipt_json BLOB NOT NULL);`)
	return err
}

// Missing map entries are never deletion; a present nil explicitly clears Club.
func applyMemberClubs(ctx context.Context, ex execer, eventID string, clubs map[string]*string) (int, error) {
	count := 0
	for memberID, club := range clubs {
		club, err := domain.NormalizeClub(club)
		if err != nil {
			return count, err
		}
		result, err := ex.ExecContext(ctx, `INSERT INTO member_club_metadata(event_id,member_id,club)
   SELECT event_id,id,? FROM members WHERE event_id=? AND id=?
   ON CONFLICT(event_id,member_id) DO UPDATE SET club=excluded.club
   WHERE member_club_metadata.club IS NOT excluded.club`, club, eventID, memberID)
		if err != nil {
			return count, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return count, err
		}
		count += int(changed)
	}
	return count, nil
}

// ApplyMemberClubs changes display metadata only, retaining all timing/profile facts.
func (s *Store) ApplyMemberClubs(ctx context.Context, eventID string, clubs map[string]*string) (int, error) {
	count := 0
	err := s.WithinTx(ctx, func(tx *Store) error {
		var err error
		count, err = applyMemberClubs(ctx, tx.db, eventID, clubs)
		return err
	})
	return count, err
}

// MemberClubs returns explicit metadata, including null clear markers.
func (s *Store) MemberClubs(ctx context.Context, eventID string) (map[string]*string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT member_id,club FROM member_club_metadata WHERE event_id=?`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	clubs := make(map[string]*string)
	for rows.Next() {
		var id string
		var club *string
		if err := rows.Scan(&id, &club); err != nil {
			return nil, err
		}
		clubs[id] = club
	}
	return clubs, rows.Err()
}

// updateMemberClubField presents the detached value as the member's Club to the
// existing audited editor/replay, without adding it to canonical packet Person.
func (s *Store) updateMemberClubField(ctx context.Context, memberID string, value any) (any, error) {
	var old any
	err := s.WithinTx(ctx, func(tx *Store) error {
		var eventID string
		if err := tx.db.QueryRowContext(ctx, `SELECT m.event_id,c.club FROM members m LEFT JOIN member_club_metadata c ON c.member_id=m.id AND c.event_id=m.event_id WHERE m.id=?`, memberID).Scan(&eventID, &old); err != nil {
			return err
		}
		var club *string
		if value != nil {
			text, ok := value.(string)
			if !ok {
				return fmt.Errorf("club expects text or null")
			}
			club = &text
		}
		_, err := applyMemberClubs(ctx, tx.db, eventID, map[string]*string{memberID: club})
		return err
	})
	return old, err
}

// MemberClubsForIDs bounds lookup cost to the registrations in one command/feed page.
func (s *Store) MemberClubsForIDs(ctx context.Context, eventID string, ids []string) (map[string]*string, error) {
	clubs := map[string]*string{}
	if len(ids) == 0 {
		return clubs, nil
	}
	args := []any{eventID}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT member_id,club FROM member_club_metadata WHERE event_id=? AND member_id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var club *string
		if err := rows.Scan(&id, &club); err != nil {
			return nil, err
		}
		clubs[id] = club
	}
	return clubs, rows.Err()
}

// ClubOwnerBinding uses the canonical owner's identity without changing Person JSON.
// A missing binding means this database has no active packet issuance scope.
func (s *Store) ClubOwnerBinding(ctx context.Context, memberID string) (json.RawMessage, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT p.value_json FROM members m
 JOIN packet_issuance_scopes sc ON sc.event_id=m.event_id
 LEFT JOIN packet_issuance_registrations p ON p.registration_id=m.id AND p.event_id=m.event_id AND p.deleted=0
 WHERE m.id=?`, memberID).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var row struct {
		Person *struct {
			ID string `json:"id"`
		} `json:"person"`
	}
	if err := json.Unmarshal([]byte(raw), &row); err != nil {
		return nil, err
	}
	var id *string
	if row.Person != nil {
		id = &row.Person.ID
	}
	return json.Marshal(id)
}

func (s *Store) ClubChangeOwnerMatches(ctx context.Context, c LocalChange) (bool, error) {
	current, err := s.ClubOwnerBinding(ctx, c.EntityID)
	if err != nil {
		return false, err
	}
	return string(current) == string(c.ClubPersonID), nil
}

// Import affiliation only for the canonical owner observed inside this transaction.
func filterClubOwners(ctx context.Context, ex database, eventID string, clubs map[string]*string, owners map[string]*string) (map[string]*string, error) {
	var scopes int
	if err := ex.QueryRowContext(ctx, `SELECT COUNT(*) FROM packet_issuance_scopes WHERE event_id=?`, eventID).Scan(&scopes); err != nil {
		return nil, err
	}
	if scopes == 0 {
		return clubs, nil
	}
	rows, err := ex.QueryContext(ctx, `SELECT registration_id,value_json FROM packet_issuance_registrations WHERE event_id=? AND deleted=0`, eventID)
	if err != nil {
		return nil, err
	}
	result := map[string]*string{}
	for rows.Next() {
		var id, raw string
		if err = rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		club, provided := clubs[id]
		owner, bound := owners[id]
		if !provided || !bound {
			continue
		}
		var row struct {
			Person *struct {
				ID string `json:"id"`
			} `json:"person"`
		}
		if err = json.Unmarshal([]byte(raw), &row); err != nil {
			rows.Close()
			return nil, err
		}
		if row.Person == nil && owner == nil && club == nil || row.Person != nil && owner != nil && row.Person.ID == *owner {
			result[id] = club
		}
	}
	err = rows.Err()
	rows.Close()
	return result, err
}

func (s *Store) ApplyBoundMemberClubs(ctx context.Context, eventID string, clubs map[string]*string, owners map[string]*string) (int, error) {
	count := 0
	err := s.WithinTx(ctx, func(tx *Store) error {
		filtered, err := filterClubOwners(ctx, tx.db, eventID, clubs, owners)
		if err != nil {
			return err
		}
		if err := tx.protectPendingPacketClubs(ctx, eventID, filtered, owners); err != nil {
			return err
		}
		count, err = applyMemberClubs(ctx, tx.db, eventID, filtered)
		return err
	})
	return count, err
}

// PacketClubOwners reads the binding in one query without timing-evidence scans.
func (s *Store) PacketClubOwners(ctx context.Context, eventID string) (map[string]*string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT p.registration_id,p.value_json FROM packet_issuance_registrations p JOIN packet_issuance_scopes sc ON sc.event_id=p.event_id WHERE p.event_id=? AND p.deleted=0`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]*string{}
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var row struct {
			Person *struct {
				ID string `json:"id"`
			} `json:"person"`
		}
		if err := json.Unmarshal([]byte(raw), &row); err != nil {
			return nil, err
		}
		var owner *string
		if row.Person != nil {
			owner = &row.Person.ID
		}
		result[id] = owner
	}
	return result, rows.Err()
}

type PacketClubEditReceipt struct {
	OperationID, ConnectionID, EventID, ScopeID, OriginInstanceID, ContentHash string
	OperationJSON, ReceiptJSON                                                 []byte
}

func (s *Store) FindPacketClubEditReceipt(ctx context.Context, id string) (*PacketClubEditReceipt, error) {
	row := PacketClubEditReceipt{OperationID: id}
	err := s.db.QueryRowContext(ctx, `SELECT connection_id,event_id,scope_id,origin_instance_id,content_hash,operation_json,receipt_json FROM packet_club_edit_receipts WHERE operation_id=?`, id).Scan(&row.ConnectionID, &row.EventID, &row.ScopeID, &row.OriginInstanceID, &row.ContentHash, &row.OperationJSON, &row.ReceiptJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}
func (s *Store) InsertPacketClubEditReceipt(ctx context.Context, row PacketClubEditReceipt) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO packet_club_edit_receipts(operation_id,connection_id,event_id,scope_id,origin_instance_id,content_hash,operation_json,receipt_json) VALUES(?,?,?,?,?,?,?,?)`, row.OperationID, row.ConnectionID, row.EventID, row.ScopeID, row.OriginInstanceID, row.ContentHash, row.OperationJSON, row.ReceiptJSON)
	return err
}
