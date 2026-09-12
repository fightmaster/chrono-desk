package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/packetissuance"
)

type PacketIssuanceScope struct {
	EventID     string
	ScopeID     string
	BaselineID  string
	SourceKind  string
	InstalledAt int64
}

type PacketOperationRecord struct {
	OperationID      string
	EventID          string
	ScopeID          string
	BaselineID       string
	OriginInstanceID string
	OriginSequence   int64
	ContentHash      string
	OperationJSON    []byte
	Outcome          string
	OutcomeCode      *string
	RecordedAt       int64
}

// InstallPacketIssuanceRoster creates the Desk projection from the site's
// authenticated bootstrap. It refuses replacement once any issuance history
// exists; later changes must arrive through the feed rather than a snapshot.
func (s *Store) InstallPacketIssuanceRoster(ctx context.Context, scope PacketIssuanceScope, rows []packetissuance.Registration) error {
	if scope.EventID == "" || scope.ScopeID == "" || scope.BaselineID == "" || scope.SourceKind != "site" || len(rows) > 20_000 {
		return fmt.Errorf("invalid packet issuance roster")
	}
	return s.WithinTx(ctx, func(txStore *Store) error {
		var history int
		if err := txStore.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM packet_issuance_operations WHERE event_id=?`, scope.EventID).Scan(&history); err != nil {
			return fmt.Errorf("count packet issuance history: %w", err)
		}
		if history != 0 {
			return fmt.Errorf("packet issuance snapshot replacement requires rebase")
		}
		var eventCount int
		if err := txStore.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE id=?`, scope.EventID).Scan(&eventCount); err != nil || eventCount != 1 {
			return fmt.Errorf("packet issuance event is not installed")
		}
		var memberCount int
		if err := txStore.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM members WHERE event_id=?`, scope.EventID).Scan(&memberCount); err != nil {
			return fmt.Errorf("count packet issuance members: %w", err)
		}
		if memberCount != len(rows) {
			return fmt.Errorf("packet issuance roster is incomplete")
		}
		seen := make(map[string]bool, len(rows))
		for _, row := range rows {
			if err := packetissuance.ValidateRegistration(row); err != nil || row.EventID != scope.EventID || seen[row.ID] {
				return fmt.Errorf("invalid packet issuance registration %s", row.ID)
			}
			seen[row.ID] = true
			if err := txStore.matchPacketMember(ctx, row); err != nil {
				return err
			}
		}
		if _, err := txStore.db.ExecContext(ctx, `DELETE FROM packet_issuance_registrations WHERE event_id=?`, scope.EventID); err != nil {
			return fmt.Errorf("clear packet issuance roster: %w", err)
		}
		for _, row := range rows {
			encoded, err := json.Marshal(row)
			if err != nil {
				return fmt.Errorf("encode packet registration %s: %w", row.ID, err)
			}
			if _, err := txStore.db.ExecContext(ctx, `INSERT INTO packet_issuance_registrations
				(registration_id,event_id,value_json,heads_json) VALUES (?,?,?,'[]')`, row.ID, scope.EventID, encoded); err != nil {
				return fmt.Errorf("insert packet registration %s: %w", row.ID, err)
			}
		}
		installedAt := scope.InstalledAt
		if installedAt == 0 {
			installedAt = time.Now().UnixMilli()
		}
		_, err := txStore.db.ExecContext(ctx, `INSERT INTO packet_issuance_scopes
			(event_id,scope_id,baseline_id,source_kind,installed_at) VALUES (?,?,?,?,?)
			ON CONFLICT(event_id) DO UPDATE SET scope_id=excluded.scope_id,baseline_id=excluded.baseline_id,
			source_kind=excluded.source_kind,installed_at=excluded.installed_at`,
			scope.EventID, scope.ScopeID, scope.BaselineID, scope.SourceKind, installedAt)
		if err != nil {
			return fmt.Errorf("save packet issuance scope: %w", err)
		}
		return nil
	})
}

func (s *Store) matchPacketMember(ctx context.Context, row packetissuance.Registration) error {
	var eventID, raceID string
	var number sql.NullInt64
	var epc sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT event_id,race_id,number,epc FROM members WHERE id=?`, row.ID).Scan(&eventID, &raceID, &number, &epc)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("packet registration %s is not in event export", row.ID)
	}
	if err != nil {
		return fmt.Errorf("read packet registration %s: %w", row.ID, err)
	}
	if eventID != row.EventID || raceID != row.RaceID || nullablePacketNumber(number) != normalizedPacketBib(row.Bib) || epc.String != row.EPC {
		return fmt.Errorf("packet registration %s does not match event export", row.ID)
	}
	return nil
}

func nullablePacketNumber(number sql.NullInt64) string {
	if !number.Valid {
		return ""
	}
	return strconv.FormatInt(number.Int64, 10)
}
func normalizedPacketBib(bib string) string {
	if bib == "" {
		return ""
	}
	n, err := strconv.ParseInt(bib, 10, 64)
	if err != nil {
		return bib
	}
	return strconv.FormatInt(n, 10)
}

func (s *Store) GetPacketIssuanceScope(ctx context.Context, eventID string) (PacketIssuanceScope, error) {
	var scope PacketIssuanceScope
	err := s.db.QueryRowContext(ctx, `SELECT event_id,scope_id,baseline_id,source_kind,installed_at FROM packet_issuance_scopes WHERE event_id=?`, eventID).
		Scan(&scope.EventID, &scope.ScopeID, &scope.BaselineID, &scope.SourceKind, &scope.InstalledAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PacketIssuanceScope{}, nil
	}
	if err != nil {
		return PacketIssuanceScope{}, fmt.Errorf("get packet issuance scope: %w", err)
	}
	return scope, nil
}

func (s *Store) ListPacketRegistrations(ctx context.Context, eventID string) ([]packetissuance.Registration, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT value_json FROM packet_issuance_registrations WHERE event_id=? ORDER BY registration_id`, eventID)
	if err != nil {
		return nil, err
	}
	result := []packetissuance.Registration{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var row packetissuance.Registration
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, fmt.Errorf("decode packet registration: %w", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// The Store intentionally has one SQL connection. Finish the roster cursor
	// before issuing per-row evidence queries or the pool would self-deadlock.
	for index := range result {
		evidence, err := s.packetTimingEvidence(ctx, result[index])
		if err != nil {
			return nil, err
		}
		result[index].HasTimingEvidence = result[index].HasTimingEvidence || evidence
	}
	return result, nil
}

func (s *Store) GetPacketRegistrations(ctx context.Context, eventID string, ids []string) ([]packetissuance.Registration, error) {
	if len(ids) < 1 || len(ids) > 2 {
		return nil, fmt.Errorf("packet operation must affect one or two registrations")
	}
	ordered := append([]string(nil), ids...)
	sort.Strings(ordered)
	result := make(map[string]packetissuance.Registration, len(ids))
	for _, id := range ordered {
		var raw []byte
		if err := s.db.QueryRowContext(ctx, `SELECT value_json FROM packet_issuance_registrations WHERE event_id=? AND registration_id=?`, eventID, id).Scan(&raw); err != nil {
			return nil, err
		}
		var row packetissuance.Registration
		if err := json.Unmarshal(raw, &row); err != nil {
			return nil, err
		}
		evidence, err := s.packetTimingEvidence(ctx, row)
		if err != nil {
			return nil, err
		}
		row.HasTimingEvidence = row.HasTimingEvidence || evidence
		result[id] = row
	}
	out := make([]packetissuance.Registration, 0, len(ids))
	for _, id := range ids {
		out = append(out, result[id])
	}
	return out, nil
}

func (s *Store) packetTimingEvidence(ctx context.Context, row packetissuance.Registration) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT CASE WHEN m.finish_time_ms IS NOT NULL OR m.clean_time IS NOT NULL
		OR m.start_observation_id IS NOT NULL OR (m.start_time_ms IS NOT NULL AND m.start_time_source<>'race_default')
		OR EXISTS(SELECT 1 FROM results r WHERE r.member_id=m.id)
		OR EXISTS(SELECT 1 FROM rfid_logs l WHERE l.event_id=m.event_id AND ((m.number IS NOT NULL AND l.number=m.number) OR (m.epc IS NOT NULL AND m.epc<>'' AND l.epc=m.epc)))
		THEN 1 ELSE 0 END FROM members m WHERE m.id=? AND m.event_id=?`, row.ID, row.EventID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("read packet timing evidence %s: %w", row.ID, err)
	}
	return count != 0, nil
}

func (s *Store) FindPacketOperation(ctx context.Context, operationID string) (PacketOperationRecord, bool, error) {
	return s.findPacketOperation(ctx, `operation_id=?`, operationID)
}
func (s *Store) FindPacketOriginSequence(ctx context.Context, origin string, sequence int64) (PacketOperationRecord, bool, error) {
	return s.findPacketOperation(ctx, `origin_instance_id=? AND origin_sequence=?`, origin, sequence)
}
func (s *Store) findPacketOperation(ctx context.Context, where string, args ...any) (PacketOperationRecord, bool, error) {
	var row PacketOperationRecord
	err := s.db.QueryRowContext(ctx, `SELECT operation_id,event_id,scope_id,baseline_id,origin_instance_id,origin_sequence,content_hash,operation_json,outcome,outcome_code,recorded_at FROM packet_issuance_operations WHERE `+where, args...).Scan(&row.OperationID, &row.EventID, &row.ScopeID, &row.BaselineID, &row.OriginInstanceID, &row.OriginSequence, &row.ContentHash, &row.OperationJSON, &row.Outcome, &row.OutcomeCode, &row.RecordedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PacketOperationRecord{}, false, nil
	}
	if err != nil {
		return PacketOperationRecord{}, false, err
	}
	return row, true, nil
}

func (s *Store) PacketDependenciesApplied(ctx context.Context, eventID string, ids []string) (bool, error) {
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return false, nil
		}
		var outcome string
		err := s.db.QueryRowContext(ctx, `SELECT outcome FROM packet_issuance_operations WHERE event_id=? AND operation_id=? UNION ALL SELECT outcome FROM packet_issuance_feed_actions WHERE event_id=? AND action_id=? LIMIT 1`, eventID, id, eventID, id).Scan(&outcome)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !strings.Contains(" applied equivalent ", " "+outcome+" ") {
			return false, nil
		}
	}
	return true, nil
}

func (s *Store) PutPacketRegistration(ctx context.Context, row packetissuance.Registration, heads []string, categoryID *string) error {
	encoded, err := json.Marshal(row)
	if err != nil {
		return err
	}
	headsJSON, err := json.Marshal(heads)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE packet_issuance_registrations SET value_json=?,heads_json=? WHERE registration_id=? AND event_id=?`, encoded, headsJSON, row.ID, row.EventID); err != nil {
		return err
	}
	var gender, dob, team, city any
	status := 0
	if row.Person != nil {
		if row.Person.Gender != "" {
			gender = row.Person.Gender
		}
		if row.Person.BirthDate != "" {
			dob = row.Person.BirthDate
		}
		if row.Person.Team != "" {
			team = row.Person.Team
		}
		if row.Person.City != "" {
			city = row.Person.City
		}
	}
	switch row.Status {
	case "dns":
		status = 1
	case "dnf":
		status = 2
	case "dsq":
		status = 3
	}
	first, last := "", ""
	if row.Person != nil {
		first, last = row.Person.FirstName, row.Person.LastName
	}
	result, err := s.db.ExecContext(ctx, `UPDATE members SET first_name=?,last_name=?,gender=?,dob=?,team=?,city=?,status=?,category_id=? WHERE id=? AND event_id=?`, first, last, gender, dob, team, city, status, categoryID, row.ID, row.EventID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return fmt.Errorf("packet member %s projection failed", row.ID)
	}
	return nil
}

func (s *Store) SavePacketOperation(ctx context.Context, operation packetissuance.Operation, hash, outcome string, code *string, recordedAt int64, known bool) error {
	raw := operation.CanonicalJSON()
	if known {
		_, err := s.db.ExecContext(ctx, `UPDATE packet_issuance_operations SET outcome=?,outcome_code=? WHERE operation_id=?`, outcome, code, operation.OperationID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO packet_issuance_operations(operation_id,event_id,scope_id,baseline_id,origin_instance_id,origin_sequence,content_hash,operation_json,outcome,outcome_code,recorded_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, operation.OperationID, operation.Changes[0].Before.EventID, operation.ScopeID, operation.BaselineID, operation.OriginInstanceID, operation.OriginSequence, hash, raw, outcome, code, recordedAt)
	return err
}

func (s *Store) PublishPacketOperation(ctx context.Context, operation packetissuance.Operation, outcome string, code *string, changes []packetissuance.Change, recordedAt int64) error {
	if outcome == "waiting_dependency" || outcome == "rejected" {
		return nil
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM packet_issuance_feed_actions WHERE source_operation_id=?`, operation.OperationID).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return nil
	}
	encoded, err := json.Marshal(withoutTimingEvidence(changes))
	if err != nil {
		return err
	}
	eventID := operation.Changes[0].Before.EventID
	if _, err := s.db.ExecContext(ctx, `INSERT INTO packet_issuance_feed_heads(event_id,last_sequence) VALUES(?,0) ON CONFLICT(event_id) DO NOTHING`, eventID); err != nil {
		return err
	}
	var sequence int64
	if err := s.db.QueryRowContext(ctx, `SELECT last_sequence+1 FROM packet_issuance_feed_heads WHERE event_id=?`, eventID).Scan(&sequence); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO packet_issuance_feed_actions(action_id,event_id,event_sequence,kind,source_code,source_operation_id,outcome,outcome_code,changes_json,recorded_at) VALUES(?,?,?,'operation','pwa.packet_issuance',?,?,?,?,?)`, operation.OperationID, eventID, sequence, operation.OperationID, outcome, code, encoded, recordedAt); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE packet_issuance_feed_heads SET last_sequence=? WHERE event_id=?`, sequence, eventID)
	return err
}

type packetFeedRegistration struct {
	ID            string                 `json:"id"`
	EventID       string                 `json:"eventId"`
	RaceID        string                 `json:"raceId"`
	Bib           string                 `json:"bib"`
	EPC           string                 `json:"epc"`
	Person        *packetissuance.Person `json:"person"`
	Reserve       bool                   `json:"reserve"`
	Issued        bool                   `json:"issued"`
	Status        string                 `json:"status"`
	TransferredTo *string                `json:"transferredTo"`
}
type packetFeedChange struct {
	RegistrationID string                 `json:"registrationId"`
	Before         packetFeedRegistration `json:"before"`
	After          packetFeedRegistration `json:"after"`
}

func withoutTimingEvidence(changes []packetissuance.Change) []packetFeedChange {
	out := make([]packetFeedChange, 0, len(changes))
	for _, c := range changes {
		convert := func(r packetissuance.Registration) packetFeedRegistration {
			return packetFeedRegistration{r.ID, r.EventID, r.RaceID, r.Bib, r.EPC, r.Person, r.Reserve, r.Issued, r.Status, r.TransferredTo}
		}
		out = append(out, packetFeedChange{c.RegistrationID, convert(c.Before), convert(c.After)})
	}
	return out
}
