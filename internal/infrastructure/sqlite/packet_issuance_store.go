package sqlite

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/packetissuance"
)

type PacketIssuanceScope struct {
	EventID        string
	ScopeID        string
	BaselineID     string
	SourceKind     string
	InstalledAt    int64
	SiteFeedCursor string
}

type PacketRegistrationRecord struct {
	Value   packetissuance.Registration
	Heads   []string
	Found   bool
	Deleted bool
}

type PacketSiteRegistrationRecord struct {
	Value   packetissuance.Registration
	Found   bool
	Deleted bool
}

type PacketSnapshotRebaseRecord struct {
	EventID          string
	ScopeID          string
	BeforeBaselineID string
	AfterBaselineID  string
	BeforeCursor     string
	AfterCursor      string
	SnapshotJSON     []byte
	ApplicationsJSON []byte
	RecordedAt       int64
}

type PacketSiteFeedRecord struct {
	ActionID        string
	EventID         string
	SiteSequence    string
	ActionJSON      []byte
	Application     string
	ApplicationCode *string
	RecordedAt      int64
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

type PacketResolutionRecord struct {
	ResolutionOperationID string
	EventID               string
	InputOperationID      string
	KeepInput             bool
	Reason                string
	RecordedAt            int64
}

type PacketConflictRecord struct {
	Operation PacketOperationRecord
	Resolved  bool
}

func (s *Store) ListPacketConflicts(ctx context.Context, eventID string, unresolvedOnly bool, limit, offset int) ([]PacketConflictRecord, int, error) {
	if limit < 1 || limit > 100 || offset < 0 {
		return nil, 0, errors.New("invalid packet conflict page")
	}
	filter := ""
	if unresolvedOnly {
		filter = " AND r.input_operation_id IS NULL"
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM packet_issuance_operations o
		LEFT JOIN packet_issuance_resolutions r ON r.input_operation_id=o.operation_id
		WHERE o.event_id=? AND o.outcome='conflict'`+filter, eventID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count packet conflicts: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT o.operation_id,o.event_id,o.scope_id,o.baseline_id,
		o.origin_instance_id,o.origin_sequence,o.content_hash,o.operation_json,o.outcome,o.outcome_code,o.recorded_at,
		CASE WHEN r.input_operation_id IS NULL THEN 0 ELSE 1 END
		FROM packet_issuance_operations o
		LEFT JOIN packet_issuance_resolutions r ON r.input_operation_id=o.operation_id
		WHERE o.event_id=? AND o.outcome='conflict'`+filter+`
		ORDER BY o.recorded_at DESC,o.operation_id DESC LIMIT ? OFFSET ?`, eventID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list packet conflicts: %w", err)
	}
	defer rows.Close()
	result := make([]PacketConflictRecord, 0)
	for rows.Next() {
		var record PacketConflictRecord
		if err := rows.Scan(&record.Operation.OperationID, &record.Operation.EventID, &record.Operation.ScopeID,
			&record.Operation.BaselineID, &record.Operation.OriginInstanceID, &record.Operation.OriginSequence,
			&record.Operation.ContentHash, &record.Operation.OperationJSON, &record.Operation.Outcome,
			&record.Operation.OutcomeCode, &record.Operation.RecordedAt, &record.Resolved); err != nil {
			return nil, 0, err
		}
		result = append(result, record)
	}
	return result, total, rows.Err()
}

type PacketFeedRow struct {
	ActionID      string
	Sequence      int64
	Kind          string
	SourceCode    string
	OperationJSON []byte
	Outcome       string
	OutcomeCode   *string
	ChangesJSON   []byte
	RecordedAt    int64
}

type PacketFeedBounds struct {
	Head           int64
	FirstAvailable int64
}

type PacketFeedArchiveResult struct {
	Archived       int
	FirstAvailable int64
}

func (s *Store) PacketFeedHead(ctx context.Context, eventID string) (int64, error) {
	var head int64
	err := s.db.QueryRowContext(ctx, `SELECT last_sequence FROM packet_issuance_feed_heads WHERE event_id=?`, eventID).Scan(&head)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return head, err
}

func (s *Store) PacketFeedBounds(ctx context.Context, eventID string) (PacketFeedBounds, error) {
	var bounds PacketFeedBounds
	err := s.db.QueryRowContext(ctx, `SELECT last_sequence,first_available_sequence
		FROM packet_issuance_feed_heads WHERE event_id=?`, eventID).Scan(&bounds.Head, &bounds.FirstAvailable)
	if errors.Is(err, sql.ErrNoRows) {
		return PacketFeedBounds{FirstAvailable: 1}, nil
	}
	if err != nil {
		return PacketFeedBounds{}, fmt.Errorf("read packet feed bounds: %w", err)
	}
	if bounds.FirstAvailable < 1 || bounds.FirstAvailable > bounds.Head+1 {
		return PacketFeedBounds{}, errors.New("invalid packet feed bounds")
	}
	return bounds, nil
}

// ArchivePacketFeedPrefix moves one contiguous bounded prefix out of the live
// transport table. Callers own the transaction and operational safety checks.
func (s *Store) ArchivePacketFeedPrefix(ctx context.Context, eventID string, through, archivedAt int64) (PacketFeedArchiveResult, error) {
	bounds, err := s.PacketFeedBounds(ctx, eventID)
	if err != nil {
		return PacketFeedArchiveResult{}, err
	}
	if through < bounds.FirstAvailable || through > bounds.Head || through-bounds.FirstAvailable+1 > 100 || archivedAt <= 0 {
		return PacketFeedArchiveResult{}, errors.New("invalid packet feed archive range")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT action_id,event_sequence,kind,source_code,source_operation_id,
		outcome,outcome_code,changes_json,recorded_at FROM packet_issuance_feed_actions
		WHERE event_id=? AND event_sequence BETWEEN ? AND ? ORDER BY event_sequence`, eventID, bounds.FirstAvailable, through)
	if err != nil {
		return PacketFeedArchiveResult{}, err
	}
	type archiveRow struct {
		actionID, kind, sourceCode, outcome string
		sequence, recordedAt                int64
		sourceOperationID, outcomeCode      sql.NullString
		changes                             []byte
	}
	batch := make([]archiveRow, 0, through-bounds.FirstAvailable+1)
	for rows.Next() {
		var row archiveRow
		if err := rows.Scan(&row.actionID, &row.sequence, &row.kind, &row.sourceCode, &row.sourceOperationID,
			&row.outcome, &row.outcomeCode, &row.changes, &row.recordedAt); err != nil {
			rows.Close()
			return PacketFeedArchiveResult{}, err
		}
		batch = append(batch, row)
	}
	if err := rows.Close(); err != nil {
		return PacketFeedArchiveResult{}, err
	}
	want := int(through - bounds.FirstAvailable + 1)
	if len(batch) != want {
		return PacketFeedArchiveResult{}, errors.New("packet feed archive gap")
	}
	for index, row := range batch {
		if row.sequence != bounds.FirstAvailable+int64(index) {
			return PacketFeedArchiveResult{}, errors.New("packet feed archive gap")
		}
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		if _, err := writer.Write(row.changes); err != nil {
			return PacketFeedArchiveResult{}, err
		}
		if err := writer.Close(); err != nil {
			return PacketFeedArchiveResult{}, err
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO packet_issuance_feed_archives
			(action_id,event_id,event_sequence,kind,source_code,source_operation_id,outcome,outcome_code,
			changes_gzip,recorded_at,archived_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, row.actionID, eventID,
			row.sequence, row.kind, row.sourceCode, row.sourceOperationID, row.outcome, row.outcomeCode,
			compressed.Bytes(), row.recordedAt, archivedAt); err != nil {
			return PacketFeedArchiveResult{}, fmt.Errorf("archive packet feed action: %w", err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM packet_issuance_feed_actions
		WHERE event_id=? AND event_sequence BETWEEN ? AND ?`, eventID, bounds.FirstAvailable, through); err != nil {
		return PacketFeedArchiveResult{}, err
	}
	next := through + 1
	result, err := s.db.ExecContext(ctx, `UPDATE packet_issuance_feed_heads SET first_available_sequence=?
		WHERE event_id=? AND first_available_sequence=? AND last_sequence=?`, next, eventID, bounds.FirstAvailable, bounds.Head)
	if err != nil {
		return PacketFeedArchiveResult{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return PacketFeedArchiveResult{}, errors.New("packet feed bounds changed")
	}
	return PacketFeedArchiveResult{Archived: len(batch), FirstAvailable: next}, nil
}

func (s *Store) ListPacketFeedRows(ctx context.Context, eventID string, after int64, limit int) ([]PacketFeedRow, error) {
	if after < 0 || limit < 1 || limit > 100 {
		return nil, errors.New("invalid packet feed query")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT f.action_id,f.event_sequence,f.kind,f.source_code,
		COALESCE(o.operation_json,''),f.outcome,f.outcome_code,f.changes_json,f.recorded_at
		FROM packet_issuance_feed_actions f
		LEFT JOIN packet_issuance_operations o ON o.operation_id=f.source_operation_id
		WHERE f.event_id=? AND f.event_sequence>? ORDER BY f.event_sequence LIMIT ?`, eventID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []PacketFeedRow{}
	for rows.Next() {
		var row PacketFeedRow
		var operation string
		if err := rows.Scan(&row.ActionID, &row.Sequence, &row.Kind, &row.SourceCode, &operation,
			&row.Outcome, &row.OutcomeCode, &row.ChangesJSON, &row.RecordedAt); err != nil {
			return nil, err
		}
		row.OperationJSON = []byte(operation)
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Store) ListPendingPacketOperations(ctx context.Context, eventID string, limit int) ([]packetissuance.Operation, error) {
	if limit < 1 || limit > 64 {
		return nil, fmt.Errorf("invalid packet operation delivery limit")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT operation_json FROM packet_issuance_operations
		WHERE event_id=? AND site_acknowledged=0 AND outcome<>'waiting_dependency'
		ORDER BY recorded_at,operation_id LIMIT ?`, eventID, limit)
	if err != nil {
		return nil, fmt.Errorf("list packet operations for site: %w", err)
	}
	defer rows.Close()
	operations := make([]packetissuance.Operation, 0)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		operation, err := packetissuance.ParseTrustedOperation(raw)
		if err != nil {
			return nil, fmt.Errorf("stored packet operation is invalid: %w", err)
		}
		operations = append(operations, operation)
	}
	return operations, rows.Err()
}

func (s *Store) MarkPacketOperationSiteReceipt(ctx context.Context, receipt packetissuance.Receipt) error {
	acknowledged := receipt.Outcome != "waiting_dependency"
	result, err := s.db.ExecContext(ctx, `UPDATE packet_issuance_operations SET
		site_acknowledged=?,site_outcome=?,site_outcome_code=?,site_attempts=site_attempts+1
		WHERE operation_id=? AND content_hash=?`, acknowledged, receipt.Outcome, receipt.Code,
		receipt.OperationID, receipt.ContentHash)
	if err != nil {
		return fmt.Errorf("save packet operation site receipt: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return fmt.Errorf("packet operation receipt identity mismatch")
	}
	return nil
}

// InstallPacketIssuanceRoster creates the Desk projection from the site's
// authenticated bootstrap. It refuses replacement once any issuance history
// exists; later changes must arrive through the feed rather than a snapshot.
func (s *Store) InstallPacketIssuanceRoster(ctx context.Context, scope PacketIssuanceScope, rows []packetissuance.Registration, origins ...*[]packetissuance.ReserveOrigin) error {
	if scope.SiteFeedCursor == "" {
		scope.SiteFeedCursor = "0"
	}
	if scope.EventID == "" || scope.ScopeID == "" || scope.BaselineID == "" || scope.SourceKind != "site" ||
		!validPacketFeedCursor(scope.SiteFeedCursor) || len(rows) > 20_000 {
		return fmt.Errorf("invalid packet issuance roster")
	}
	return s.WithinTx(ctx, func(txStore *Store) error {
		var history int
		if err := txStore.db.QueryRowContext(ctx, `SELECT
			(SELECT COUNT(*) FROM packet_issuance_operations WHERE event_id=?) +
			(SELECT COUNT(*) FROM packet_issuance_site_feed_actions WHERE event_id=?) +
			(SELECT COUNT(*) FROM packet_issuance_feed_actions WHERE event_id=?) +
			(SELECT COUNT(*) FROM packet_issuance_feed_archives WHERE event_id=?)`,
			scope.EventID, scope.EventID, scope.EventID, scope.EventID).Scan(&history); err != nil {
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
		if _, err := txStore.db.ExecContext(ctx, `DELETE FROM packet_issuance_site_registrations WHERE event_id=?`, scope.EventID); err != nil {
			return fmt.Errorf("clear packet issuance site baseline: %w", err)
		}
		for _, row := range rows {
			encoded, err := json.Marshal(row)
			if err != nil {
				return fmt.Errorf("encode packet registration %s: %w", row.ID, err)
			}
			if _, err := txStore.db.ExecContext(ctx, `INSERT INTO packet_issuance_registrations
				(registration_id,event_id,value_json,heads_json,deleted) VALUES (?,?,?,'[]',0)`, row.ID, scope.EventID, encoded); err != nil {
				return fmt.Errorf("insert packet registration %s: %w", row.ID, err)
			}
			if _, err := txStore.db.ExecContext(ctx, `INSERT INTO packet_issuance_site_registrations
				(event_id,registration_id,value_json,deleted) VALUES (?,?,?,0)`, scope.EventID, row.ID, encoded); err != nil {
				return fmt.Errorf("insert packet site baseline %s: %w", row.ID, err)
			}
		}
		if len(origins) != 0 {
			values := origins[0]
			if values != nil {
				combined := packetissuance.MatchingReserveOrigins(rows, *values)
				if err := txStore.SavePacketReserveOrigins(ctx, scope.EventID, &combined); err != nil {
					return err
				}
			}
		}
		installedAt := scope.InstalledAt
		if installedAt == 0 {
			installedAt = time.Now().UnixMilli()
		}
		_, err := txStore.db.ExecContext(ctx, `INSERT INTO packet_issuance_scopes
			(event_id,scope_id,baseline_id,source_kind,installed_at,site_feed_cursor) VALUES (?,?,?,?,?,?)
			ON CONFLICT(event_id) DO UPDATE SET scope_id=excluded.scope_id,baseline_id=excluded.baseline_id,
			source_kind=excluded.source_kind,installed_at=excluded.installed_at,site_feed_cursor=excluded.site_feed_cursor`,
			scope.EventID, scope.ScopeID, scope.BaselineID, scope.SourceKind, installedAt, scope.SiteFeedCursor)
		if err != nil {
			return fmt.Errorf("save packet issuance scope: %w", err)
		}
		return nil
	})
}

func validPacketFeedCursor(value string) bool {
	parsed, err := strconv.ParseInt(value, 10, 64)
	return err == nil && parsed >= 0 && strconv.FormatInt(parsed, 10) == value
}

func (s *Store) matchPacketMember(ctx context.Context, row packetissuance.Registration) error {
	var eventID, raceID string
	var number sql.NullInt64
	var epc, gender, dob, team, city sql.NullString
	var firstName, lastName string
	var status int
	err := s.db.QueryRowContext(ctx, `SELECT event_id,race_id,number,epc,first_name,last_name,gender,dob,team,city,status
		FROM members WHERE id=?`, row.ID).
		Scan(&eventID, &raceID, &number, &epc, &firstName, &lastName, &gender, &dob, &team, &city, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("packet registration %s is not in event export", row.ID)
	}
	if err != nil {
		return fmt.Errorf("read packet registration %s: %w", row.ID, err)
	}
	profileMatches := row.Person == nil && strings.TrimSpace(firstName+lastName) == ""
	if row.Person != nil {
		profileMatches = row.Person.FirstName == firstName && row.Person.LastName == lastName &&
			row.Person.Gender == gender.String && row.Person.BirthDate == dob.String &&
			row.Person.Team == team.String && row.Person.City == city.String
	}
	if eventID != row.EventID || raceID != row.RaceID || nullablePacketNumber(number) != normalizedPacketBib(row.Bib) ||
		epc.String != row.EPC || packetStatusFromInt(status) != row.Status || !profileMatches {
		return fmt.Errorf("packet registration %s does not match event export", row.ID)
	}
	return nil
}

func packetStatusFromInt(status int) string {
	switch status {
	case 1:
		return "dns"
	case 2:
		return "dnf"
	case 3:
		return "dsq"
	default:
		return "registered"
	}
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
	err := s.db.QueryRowContext(ctx, `SELECT event_id,scope_id,baseline_id,source_kind,installed_at,site_feed_cursor FROM packet_issuance_scopes WHERE event_id=?`, eventID).
		Scan(&scope.EventID, &scope.ScopeID, &scope.BaselineID, &scope.SourceKind, &scope.InstalledAt, &scope.SiteFeedCursor)
	if errors.Is(err, sql.ErrNoRows) {
		return PacketIssuanceScope{}, nil
	}
	if err != nil {
		return PacketIssuanceScope{}, fmt.Errorf("get packet issuance scope: %w", err)
	}
	return scope, nil
}

func (s *Store) ListPacketRegistrations(ctx context.Context, eventID string) ([]packetissuance.Registration, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT value_json FROM packet_issuance_registrations WHERE event_id=? AND deleted=0 ORDER BY registration_id`, eventID)
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
		if err := s.db.QueryRowContext(ctx, `SELECT value_json FROM packet_issuance_registrations WHERE event_id=? AND registration_id=? AND deleted=0`, eventID, id).Scan(&raw); err != nil {
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

func (s *Store) FindPacketRegistration(ctx context.Context, eventID, id string) (PacketRegistrationRecord, error) {
	var raw, headsRaw []byte
	var deleted bool
	err := s.db.QueryRowContext(ctx, `SELECT value_json,heads_json,deleted FROM packet_issuance_registrations
		WHERE event_id=? AND registration_id=?`, eventID, id).Scan(&raw, &headsRaw, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return PacketRegistrationRecord{}, nil
	}
	if err != nil {
		return PacketRegistrationRecord{}, fmt.Errorf("find packet registration: %w", err)
	}
	var value packetissuance.Registration
	var heads []string
	if err := json.Unmarshal(raw, &value); err != nil {
		return PacketRegistrationRecord{}, fmt.Errorf("decode packet registration: %w", err)
	}
	if err := json.Unmarshal(headsRaw, &heads); err != nil {
		return PacketRegistrationRecord{}, fmt.Errorf("decode packet registration heads: %w", err)
	}
	if !deleted {
		evidence, err := s.packetTimingEvidence(ctx, value)
		if err != nil {
			return PacketRegistrationRecord{}, err
		}
		value.HasTimingEvidence = value.HasTimingEvidence || evidence
	}
	return PacketRegistrationRecord{Value: value, Heads: heads, Found: true, Deleted: deleted}, nil
}

func (s *Store) FindPacketSiteRegistration(ctx context.Context, eventID, id string) (PacketSiteRegistrationRecord, error) {
	var raw []byte
	var deleted bool
	err := s.db.QueryRowContext(ctx, `SELECT value_json,deleted FROM packet_issuance_site_registrations
		WHERE event_id=? AND registration_id=?`, eventID, id).Scan(&raw, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return PacketSiteRegistrationRecord{}, nil
	}
	if err != nil {
		return PacketSiteRegistrationRecord{}, fmt.Errorf("find packet site registration: %w", err)
	}
	var value packetissuance.Registration
	if err := json.Unmarshal(raw, &value); err != nil {
		return PacketSiteRegistrationRecord{}, fmt.Errorf("decode packet site registration: %w", err)
	}
	return PacketSiteRegistrationRecord{Value: value, Found: true, Deleted: deleted}, nil
}

func (s *Store) ListPacketSiteRegistrations(ctx context.Context, eventID string) ([]PacketSiteRegistrationRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT value_json,deleted FROM packet_issuance_site_registrations
		WHERE event_id=? ORDER BY registration_id`, eventID)
	if err != nil {
		return nil, fmt.Errorf("list packet site registrations: %w", err)
	}
	defer rows.Close()
	result := make([]PacketSiteRegistrationRecord, 0)
	for rows.Next() {
		var raw []byte
		var deleted bool
		if err := rows.Scan(&raw, &deleted); err != nil {
			return nil, err
		}
		var value packetissuance.Registration
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, fmt.Errorf("decode packet site registration: %w", err)
		}
		result = append(result, PacketSiteRegistrationRecord{Value: value, Found: true, Deleted: deleted})
	}
	return result, rows.Err()
}

func (s *Store) PacketSiteBaselineComplete(ctx context.Context, eventID string) (bool, error) {
	var complete bool
	err := s.db.QueryRowContext(ctx, `SELECT NOT EXISTS(
		SELECT 1 FROM packet_issuance_registrations r
		LEFT JOIN packet_issuance_site_registrations b
		ON b.event_id=r.event_id AND b.registration_id=r.registration_id
		WHERE r.event_id=? AND b.registration_id IS NULL)`, eventID).Scan(&complete)
	if err != nil {
		return false, fmt.Errorf("inspect packet site baseline: %w", err)
	}
	return complete, nil
}

func (s *Store) PutPacketSiteRegistration(ctx context.Context, eventID string, row *packetissuance.Registration) error {
	if row == nil {
		return errors.New("packet site deletion requires previous value")
	}
	if err := packetissuance.ValidateRegistration(*row); err != nil || row.EventID != eventID {
		return errors.New("invalid packet site registration")
	}
	encoded, err := json.Marshal(row)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO packet_issuance_site_registrations
		(event_id,registration_id,value_json,deleted) VALUES(?,?,?,0)
		ON CONFLICT(event_id,registration_id) DO UPDATE SET value_json=excluded.value_json,deleted=0`,
		eventID, row.ID, encoded)
	return err
}

func (s *Store) DeletePacketSiteRegistration(ctx context.Context, eventID string, previous packetissuance.Registration) error {
	if err := packetissuance.ValidateRegistration(previous); err != nil || previous.EventID != eventID {
		return errors.New("invalid packet site registration")
	}
	encoded, err := json.Marshal(previous)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO packet_issuance_site_registrations
		(event_id,registration_id,value_json,deleted) VALUES(?,?,?,1)
		ON CONFLICT(event_id,registration_id) DO UPDATE SET value_json=excluded.value_json,deleted=1`,
		eventID, previous.ID, encoded)
	return err
}

func (s *Store) SavePacketSnapshotRebase(ctx context.Context, row PacketSnapshotRebaseRecord) error {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM packet_issuance_snapshot_rebases WHERE event_id=?`, row.EventID).Scan(&count); err != nil {
		return err
	}
	if count >= 64 {
		return errors.New("packet_snapshot_rebase_limit")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO packet_issuance_snapshot_rebases
		(event_id,scope_id,before_baseline_id,after_baseline_id,before_cursor,after_cursor,
		snapshot_json,applications_json,recorded_at) VALUES(?,?,?,?,?,?,?,?,?)`, row.EventID, row.ScopeID,
		row.BeforeBaselineID, row.AfterBaselineID, row.BeforeCursor, row.AfterCursor,
		row.SnapshotJSON, row.ApplicationsJSON, row.RecordedAt)
	if err != nil {
		return fmt.Errorf("save packet snapshot rebase: %w", err)
	}
	return nil
}

func (s *Store) CompletePacketSnapshotRebase(ctx context.Context, eventID, expectedBaseline,
	nextBaseline, expectedCursor, nextCursor string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE packet_issuance_scopes
		SET baseline_id=?,site_feed_cursor=? WHERE event_id=? AND baseline_id=? AND site_feed_cursor=?`,
		nextBaseline, nextCursor, eventID, expectedBaseline, expectedCursor)
	if err != nil {
		return fmt.Errorf("complete packet snapshot rebase: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return errors.New("packet_feed_cursor_changed")
	}
	return nil
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
		var accepted bool
		err := s.db.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM packet_issuance_operations WHERE event_id=? AND operation_id=? AND outcome IN ('applied','equivalent')
			UNION ALL SELECT 1 FROM packet_issuance_feed_actions WHERE event_id=? AND action_id=? AND outcome IN ('applied','equivalent')
			UNION ALL SELECT 1 FROM packet_issuance_feed_archives WHERE event_id=? AND action_id=? AND outcome IN ('applied','equivalent')
			UNION ALL SELECT 1 FROM packet_issuance_site_feed_actions WHERE event_id=? AND action_id=? AND application IN ('applied','observed')
		)`, eventID, id, eventID, id, eventID, id, eventID, id).Scan(&accepted)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !accepted {
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
	if _, err := s.db.ExecContext(ctx, `UPDATE packet_issuance_registrations SET value_json=?,heads_json=?,deleted=0 WHERE registration_id=? AND event_id=?`, encoded, headsJSON, row.ID, row.EventID); err != nil {
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
	result, err := s.db.ExecContext(ctx, `UPDATE members SET race_id=?,first_name=?,last_name=?,gender=?,dob=?,team=?,city=?,status=?,category_id=?,number=?,rfid=CASE WHEN COALESCE(epc,'')<>? THEN NULL ELSE rfid END,epc=CASE WHEN COALESCE(epc,'')=? THEN epc ELSE ? END WHERE id=? AND event_id=?`, row.RaceID, first, last, gender, dob, team, city, status, categoryID, packetRegistrationMember(row, categoryID).Number, row.EPC, row.EPC, packetRegistrationMember(row, categoryID).EPC, row.ID, row.EventID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return fmt.Errorf("packet member %s projection failed", row.ID)
	}
	return nil
}

func (s *Store) InsertPacketRegistration(ctx context.Context, row packetissuance.Registration, heads []string, categoryID *string) error {
	encoded, err := json.Marshal(row)
	if err != nil {
		return err
	}
	headsJSON, err := json.Marshal(heads)
	if err != nil {
		return err
	}
	member := packetRegistrationMember(row, categoryID)
	if err := upsertMember(ctx, s.db, member); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO packet_issuance_registrations
		(registration_id,event_id,value_json,heads_json,deleted) VALUES(?,?,?,?,0)`,
		row.ID, row.EventID, encoded, headsJSON)
	if err != nil {
		return fmt.Errorf("insert packet registration projection: %w", err)
	}
	return nil
}

func packetRegistrationMember(row packetissuance.Registration, categoryID *string) domain.Member {
	member := domain.Member{ID: row.ID, EventID: row.EventID, RaceID: row.RaceID, CategoryID: categoryID}
	if number, err := strconv.ParseInt(row.Bib, 10, 64); err == nil && row.Bib != "" {
		member.Number = &number
	}
	if row.EPC != "" {
		epc := row.EPC
		member.EPC = &epc
	}
	if row.Person != nil {
		member.FirstName, member.LastName = row.Person.FirstName, row.Person.LastName
		if row.Person.Gender != "" {
			value := row.Person.Gender
			member.Gender = &value
		}
		if row.Person.BirthDate != "" {
			value := row.Person.BirthDate
			member.DOB = &value
		}
		if row.Person.Team != "" {
			value := row.Person.Team
			member.Team = &value
		}
		if row.Person.City != "" {
			value := row.Person.City
			member.City = &value
		}
	}
	switch row.Status {
	case "dns":
		member.Status = domain.StatusDNS
	case "dnf":
		member.Status = domain.StatusDNF
	case "dsq":
		member.Status = domain.StatusDSQ
	}
	return member
}

func (s *Store) DeletePacketRegistration(ctx context.Context, eventID, id string, heads []string) error {
	headsJSON, err := json.Marshal(heads)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE packet_issuance_registrations SET deleted=1,heads_json=?
		WHERE event_id=? AND registration_id=?`, headsJSON, eventID, id)
	if err != nil {
		return fmt.Errorf("delete packet registration projection: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return fmt.Errorf("packet registration %s projection failed", id)
	}
	return nil
}

func (s *Store) AdvancePacketSiteCursor(ctx context.Context, eventID, expected, next string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE packet_issuance_scopes SET site_feed_cursor=?
		WHERE event_id=? AND site_feed_cursor=?`, next, eventID, expected)
	if err != nil {
		return fmt.Errorf("advance packet site cursor: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return errors.New("packet_feed_cursor_changed")
	}
	return nil
}

func (s *Store) FindPacketSiteFeedAction(ctx context.Context, actionID string) (PacketSiteFeedRecord, bool, error) {
	var row PacketSiteFeedRecord
	err := s.db.QueryRowContext(ctx, `SELECT action_id,event_id,site_sequence,action_json,application,
		application_code,recorded_at FROM packet_issuance_site_feed_actions WHERE action_id=?`, actionID).
		Scan(&row.ActionID, &row.EventID, &row.SiteSequence, &row.ActionJSON, &row.Application,
			&row.ApplicationCode, &row.RecordedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PacketSiteFeedRecord{}, false, nil
	}
	if err != nil {
		return PacketSiteFeedRecord{}, false, fmt.Errorf("find packet site action: %w", err)
	}
	return row, true, nil
}

func (s *Store) SavePacketSiteFeedAction(ctx context.Context, row PacketSiteFeedRecord) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO packet_issuance_site_feed_actions
		(action_id,event_id,site_sequence,action_json,application,application_code,recorded_at)
		VALUES(?,?,?,?,?,?,?)`, row.ActionID, row.EventID, row.SiteSequence, row.ActionJSON,
		row.Application, row.ApplicationCode, row.RecordedAt)
	if err != nil {
		return fmt.Errorf("save packet site action: %w", err)
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

// SaveReceivedPacketOperation stores an authenticated feed operation without
// making it eligible for the outbound site journal.
func (s *Store) SaveReceivedPacketOperation(ctx context.Context, operation packetissuance.Operation, eventID,
	localOutcome string, localCode *string, siteOutcome string, siteCode *string, recordedAt int64) error {
	existing, found, err := s.FindPacketOperation(ctx, operation.OperationID)
	if err != nil {
		return err
	}
	hash := packetissuance.ContentHash(operation)
	if found {
		if existing.EventID != eventID || existing.ScopeID != operation.ScopeID || existing.ContentHash != hash ||
			!reflect.DeepEqual(existing.OperationJSON, operation.CanonicalJSON()) {
			return errors.New("packet_feed_action_identity_conflict")
		}
		_, err = s.db.ExecContext(ctx, `UPDATE packet_issuance_operations SET site_acknowledged=1,
			site_outcome=?,site_outcome_code=? WHERE operation_id=?`, siteOutcome, siteCode, operation.OperationID)
		return err
	}
	owner, occupied, err := s.FindPacketOriginSequence(ctx, operation.OriginInstanceID, operation.OriginSequence)
	if err != nil {
		return err
	}
	if occupied && owner.OperationID != operation.OperationID {
		return errors.New("packet_feed_action_identity_conflict")
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO packet_issuance_operations
		(operation_id,event_id,scope_id,baseline_id,origin_instance_id,origin_sequence,content_hash,
		 operation_json,outcome,outcome_code,recorded_at,site_acknowledged,site_outcome,site_outcome_code)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,1,?,?)`, operation.OperationID, eventID, operation.ScopeID,
		operation.BaselineID, operation.OriginInstanceID, operation.OriginSequence, hash,
		operation.CanonicalJSON(), localOutcome, localCode, recordedAt, siteOutcome, siteCode)
	return err
}

func (s *Store) FindPacketResolutionByInput(ctx context.Context, inputOperationID string) (PacketResolutionRecord, bool, error) {
	var row PacketResolutionRecord
	err := s.db.QueryRowContext(ctx, `SELECT resolution_operation_id,event_id,input_operation_id,
		keep_input,reason,recorded_at FROM packet_issuance_resolutions WHERE input_operation_id=?`, inputOperationID).
		Scan(&row.ResolutionOperationID, &row.EventID, &row.InputOperationID, &row.KeepInput, &row.Reason, &row.RecordedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PacketResolutionRecord{}, false, nil
	}
	if err != nil {
		return PacketResolutionRecord{}, false, err
	}
	return row, true, nil
}

func (s *Store) SavePacketResolution(ctx context.Context, row PacketResolutionRecord) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO packet_issuance_resolutions
		(resolution_operation_id,event_id,input_operation_id,keep_input,reason,recorded_at)
		VALUES(?,?,?,?,?,?)`, row.ResolutionOperationID, row.EventID, row.InputOperationID,
		row.KeepInput, row.Reason, row.RecordedAt)
	if err != nil {
		return fmt.Errorf("save packet resolution: %w", err)
	}
	return nil
}

// RelayPacketSiteAction exposes a site action to LAN tablets using the
// Desk-local cursor. An operation already published locally is merely another
// occurrence of the same immutable action and is not duplicated.
func (s *Store) RelayPacketSiteAction(ctx context.Context, eventID string, action packetissuance.FeedAction, recordedAt int64) error {
	var kind string
	var sourceOperation sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT kind,source_operation_id FROM packet_issuance_feed_actions WHERE action_id=?`, action.ActionID).
		Scan(&kind, &sourceOperation)
	if err == nil {
		if action.Kind == kind && (kind == "operation" || kind == "server_change") && sourceOperation.Valid && sourceOperation.String == action.ActionID {
			return nil
		}
		return errors.New("packet_feed_action_identity_conflict")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	changes, err := json.Marshal(withoutFeedTimingEvidence(action.Changes))
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO packet_issuance_feed_heads(event_id,last_sequence)
		VALUES(?,0) ON CONFLICT(event_id) DO NOTHING`, eventID); err != nil {
		return err
	}
	var sequence int64
	if err := s.db.QueryRowContext(ctx, `SELECT last_sequence+1 FROM packet_issuance_feed_heads WHERE event_id=?`, eventID).Scan(&sequence); err != nil {
		return err
	}
	var sourceOperationID any
	if action.Operation != nil {
		sourceOperationID = action.ActionID
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO packet_issuance_feed_actions
		(action_id,event_id,event_sequence,kind,source_code,source_operation_id,outcome,outcome_code,changes_json,recorded_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, action.ActionID, eventID, sequence, action.Kind, action.SourceCode,
		sourceOperationID, action.Outcome, action.Code, changes, recordedAt); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE packet_issuance_feed_heads SET last_sequence=? WHERE event_id=?`, sequence, eventID)
	return err
}

func (s *Store) PublishPacketOperation(ctx context.Context, operation packetissuance.Operation, outcome string, code *string, changes []packetissuance.Change, recordedAt int64) error {
	if outcome == "waiting_dependency" || outcome == "rejected" {
		return nil
	}
	if operation.Command.Type == "change_race" && outcome != "applied" {
		return nil
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM packet_issuance_feed_actions WHERE source_operation_id=?) +
		(SELECT COUNT(*) FROM packet_issuance_feed_archives WHERE source_operation_id=?)`,
		operation.OperationID, operation.OperationID).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return nil
	}
	var feedChanges any = withoutTimingEvidence(changes)
	if operation.Command.Type == "create_registration" || operation.Command.Type == "return_to_reserve" {
		lifecycle := make([]packetissuance.FeedChange, 0, len(changes))
		for _, change := range changes {
			before, after := change.Before, change.After
			item := packetissuance.FeedChange{RegistrationID: after.ID, Before: &before, After: &after}
			if packetissuance.IsVirtualRegistration(before) {
				item.Before = nil
			}
			lifecycle = append(lifecycle, item)
		}
		feedChanges = withoutFeedTimingEvidence(lifecycle)
	}
	if operation.SchemaVersion == 2 {
		lifecycle := make([]packetissuance.FeedChange, 0, len(changes))
		for _, change := range changes {
			before, after := change.Before, change.After
			item := packetissuance.FeedChange{RegistrationID: change.RegistrationID, Before: &before, After: &after}
			if packetissuance.IsVirtualRegistration(after) {
				item.After = nil
			}
			lifecycle = append(lifecycle, item)
		}
		feedChanges = withoutFeedTimingEvidence(lifecycle)
	}
	encoded, err := json.Marshal(feedChanges)
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
	sourceCode := "pwa.packet_issuance"
	if operation.SchemaVersion == 2 {
		sourceCode = "admin.packet_issuance_resolution"
	}
	kind := "operation"
	if operation.Command.Type == "change_race" {
		kind = "server_change"
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO packet_issuance_feed_actions(action_id,event_id,event_sequence,kind,source_code,source_operation_id,outcome,outcome_code,changes_json,recorded_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, operation.OperationID, eventID, sequence, kind, sourceCode, operation.OperationID, outcome, code, encoded, recordedAt); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE packet_issuance_feed_heads SET last_sequence=? WHERE event_id=?`, sequence, eventID)
	return err
}

// PublishPacketServerChange exposes a Desk-owned registration edit to LAN
// tablets. The caller owns the surrounding transaction and updates the member
// plus packet projection before publishing this immutable action.
func (s *Store) PublishPacketServerChange(ctx context.Context, eventID, actionID, sourceCode string, changes []packetissuance.Change, recordedAt int64) error {
	if !validPacketLANUUID(actionID) || len(changes) < 1 || len(changes) > 20_000 ||
		len(sourceCode) < 1 || len(sourceCode) > 64 {
		return errors.New("invalid packet server change")
	}
	encoded, err := json.Marshal(withoutTimingEvidence(changes))
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO packet_issuance_feed_heads(event_id,last_sequence)
		VALUES(?,0) ON CONFLICT(event_id) DO NOTHING`, eventID); err != nil {
		return err
	}
	var sequence int64
	if err := s.db.QueryRowContext(ctx, `SELECT last_sequence+1 FROM packet_issuance_feed_heads WHERE event_id=?`, eventID).Scan(&sequence); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO packet_issuance_feed_actions
		(action_id,event_id,event_sequence,kind,source_code,source_operation_id,outcome,outcome_code,changes_json,recorded_at)
		VALUES(?,?,?,'server_change',?,NULL,'applied',NULL,?,?)`, actionID, eventID, sequence, sourceCode, encoded, recordedAt); err != nil {
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

type packetRelayedFeedChange struct {
	RegistrationID string                  `json:"registrationId"`
	Before         *packetFeedRegistration `json:"before"`
	After          *packetFeedRegistration `json:"after"`
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

func withoutFeedTimingEvidence(changes []packetissuance.FeedChange) []packetRelayedFeedChange {
	convert := func(row *packetissuance.Registration) *packetFeedRegistration {
		if row == nil {
			return nil
		}
		value := packetFeedRegistration{row.ID, row.EventID, row.RaceID, row.Bib, row.EPC, row.Person,
			row.Reserve, row.Issued, row.Status, row.TransferredTo}
		return &value
	}
	out := make([]packetRelayedFeedChange, 0, len(changes))
	for _, change := range changes {
		out = append(out, packetRelayedFeedChange{RegistrationID: change.RegistrationID,
			Before: convert(change.Before), After: convert(change.After)})
	}
	return out
}

// CheckPacketNumber runs inside the receiver's SQLite write transaction.
func (s *Store) CheckPacketNumber(ctx context.Context, eventID, registrationID, bib string) (string, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM members WHERE event_id=? AND id<>? AND number=CAST(? AS INTEGER)`, eventID, registrationID, bib).Scan(&count); err != nil {
		return "", err
	}
	if count != 0 {
		return "number_occupied", nil
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rfid_logs WHERE event_id=? AND number=CAST(? AS INTEGER)`, eventID, bib).Scan(&count); err != nil {
		return "", err
	}
	if count != 0 {
		return "timing_review_required", nil
	}
	return "", nil
}

// RestorePacketRFID preserves the detached slot's legacy identifier.
func (s *Store) RestorePacketRFID(ctx context.Context, targetID string, rfid *string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE members SET rfid=? WHERE id=?`, rfid, targetID)
	return err
}
