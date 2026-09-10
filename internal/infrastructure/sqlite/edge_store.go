package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/rfid-core/edge"
	"gitlab.com/fightmaster1/rfid-core/ingest"
)

func (s *Store) EdgeBindings(ctx context.Context, eventID string) ([]domain.EdgeBinding, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT board,source_session_id FROM edge_bindings WHERE event_id=? ORDER BY board`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bindings := []domain.EdgeBinding{}
	for rows.Next() {
		var b domain.EdgeBinding
		if err := rows.Scan(&b.Board, &b.SourceSessionID); err != nil {
			return nil, err
		}
		bindings = append(bindings, b)
	}
	return bindings, rows.Err()
}

// SetEdgeBindings journals local provisioning atomically. No site edit or
// observation binding is inferred from these settings on later retransmission.
func (s *Store) SetEdgeBindings(ctx context.Context, eventID string, bindings []domain.EdgeBinding) error {
	if len(bindings) > 64 {
		return errors.New("maximum 64 edge board bindings")
	}
	seen := map[string]bool{}
	for _, b := range bindings {
		if len(b.Board) == 0 || len(b.Board) > 128 || strings.TrimSpace(b.Board) != b.Board || strings.IndexFunc(b.Board, unicode.IsControl) >= 0 || !edge.ValidToken(b.SourceSessionID) || seen[b.Board] {
			return errors.New("invalid or duplicate edge board/session binding")
		}
		seen[b.Board] = true
	}
	return s.WithinTx(ctx, func(tx *Store) error {
		var storedID string
		if err := tx.db.QueryRowContext(ctx, `SELECT id FROM events WHERE id=?`, eventID).Scan(&storedID); err != nil {
			return err
		}
		for _, b := range bindings {
			var count int
			if err := tx.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM checkpoints WHERE event_id=? AND board=?`, eventID, b.Board).Scan(&count); err != nil {
				return err
			}
			if count == 0 {
				return fmt.Errorf("board %s has no checkpoint in event %s", b.Board, eventID)
			}
		}
		previous, err := tx.EdgeBindings(ctx, eventID)
		if err != nil {
			return err
		}
		before, _ := json.Marshal(previous)
		after, _ := json.Marshal(bindings)
		if _, err := tx.db.ExecContext(ctx, `DELETE FROM edge_bindings WHERE event_id=?`, eventID); err != nil {
			return err
		}
		for _, b := range bindings {
			if _, err := tx.db.ExecContext(ctx, `INSERT INTO edge_bindings(event_id,board,source_session_id) VALUES(?,?,?)`, eventID, b.Board, b.SourceSessionID); err != nil {
				return err
			}
		}
		return tx.InsertLocalChange(ctx, LocalChange{Entity: "edge_binding", EntityID: eventID, Field: "bindings", OldValue: string(before), NewValue: string(after)})
	})
}

type EdgeAcceptance struct {
	Log      domain.RfidLog
	Inserted bool
}

// AcceptEdgeObservation must join the publisher's transaction so source facts,
// relay journal and initial projection have one commit/ACK boundary.
func (s *Store) AcceptEdgeObservation(ctx context.Context, eventID string, event ingest.Event) (EdgeAcceptance, error) {
	if s.tx == nil {
		return EdgeAcceptance{}, errors.New("edge acceptance requires an event transaction")
	}
	payload, err := edge.Encode(event)
	if err != nil {
		return EdgeAcceptance{}, err
	}
	if eventID != strconv.FormatInt(event.ExternalEventID, 10) {
		return EdgeAcceptance{}, errors.New("edge observation belongs to another event")
	}
	var session string
	if err := s.db.QueryRowContext(ctx, `SELECT source_session_id FROM edge_bindings WHERE event_id=? AND board=?`, eventID, event.Board).Scan(&session); err != nil {
		return EdgeAcceptance{}, fmt.Errorf("edge board is not provisioned: %w", err)
	}
	if session != event.SourceSessionID {
		return EdgeAcceptance{}, errors.New("edge source session does not match provisioned binding")
	}
	var checkpoints int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM checkpoints WHERE event_id=? AND board=?`, eventID, event.Board).Scan(&checkpoints); err != nil {
		return EdgeAcceptance{}, err
	}
	if checkpoints == 0 {
		return EdgeAcceptance{}, errors.New("edge board no longer has an event checkpoint")
	}
	entry := domain.RfidLog{ID: event.ID, EventID: eventID, Status: event.Status, Number: event.Number, TimeMs: event.Time, Ant: event.Ant, EPC: event.EPC, RSSI: event.RSSI, Board: event.Board,
		ObservationVersion: event.ObservationVersion, CaptureSourceID: event.CaptureSourceID, OriginSystem: event.OriginSystem, OriginInstanceID: event.OriginInstanceID, OriginSequence: int64(event.OriginSequence)}
	existing, found, err := s.findRfidLog(ctx, entry.ID)
	if err != nil {
		return EdgeAcceptance{}, err
	}
	if !found {
		// Preserve historical plate IDs instead of minting another observation for
		// the same physical crossing. Multiple existing matches are ambiguous.
		rows, err := s.db.QueryContext(ctx, `SELECT id FROM rfid_logs WHERE event_id=? AND board=? AND time_ms=? AND ant=? AND epc=? COLLATE NOCASE AND number=? LIMIT 2`, eventID, entry.Board, entry.TimeMs, entry.Ant, entry.EPC, entry.Number)
		if err != nil {
			return EdgeAcceptance{}, err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return EdgeAcceptance{}, err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return EdgeAcceptance{}, err
		}
		if len(ids) > 1 {
			return EdgeAcceptance{}, errors.New("ambiguous legacy physical observation")
		}
		if len(ids) == 1 {
			existing, found, err = s.findRfidLog(ctx, ids[0])
			if err != nil {
				return EdgeAcceptance{}, err
			}
		}
	}
	if found {
		physical := ingest.Event{ID: event.ID, Board: existing.Board, EPC: existing.EPC, Time: existing.TimeMs, Ant: existing.Ant, Number: existing.Number}
		if existing.EventID != eventID || !edge.SamePhysicalRead(event, physical) {
			return EdgeAcceptance{}, errors.New("edge observation conflicts with immutable stored content")
		}
		return EdgeAcceptance{Log: existing}, nil
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO rfid_logs(id,event_id,status,number,time_ms,ant,epc,rssi,board,observation_version,capture_source_id,origin_system,origin_instance_id,origin_sequence) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`,
		entry.ID, entry.EventID, entry.Status, entry.Number, entry.TimeMs, entry.Ant, entry.EPC, entry.RSSI, entry.Board, entry.ObservationVersion, entry.CaptureSourceID, entry.OriginSystem, entry.OriginInstanceID, entry.OriginSequence)
	if err != nil {
		return EdgeAcceptance{}, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return EdgeAcceptance{}, err
	}
	if inserted != 1 {
		return EdgeAcceptance{}, errors.New("observation changed during acceptance; retry validation")
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO edge_observation_outbox(observation_id,event_id,payload_json,created_at) VALUES(?,?,?,?)`, entry.ID, eventID, string(payload), time.Now().UnixMilli()); err != nil {
		return EdgeAcceptance{}, err
	}
	return EdgeAcceptance{Log: entry, Inserted: true}, nil
}

type EdgeJournalItem struct {
	Sequence      int64           `json:"sequence"`
	ObservationID string          `json:"observation_id"`
	Payload       json.RawMessage `json:"payload"`
	State         string          `json:"state"`
}

// EdgeJournal is bounded diagnostics/export, not permission to upload a full
// event snapshot. Native/site-imported observations never appear here.
func (s *Store) EdgeJournal(ctx context.Context, eventID string, after int64, limit int) ([]EdgeJournalItem, error) {
	if after < 0 || limit < 1 || limit > 500 {
		return nil, errors.New("invalid edge journal page")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sequence,observation_id,payload_json,state FROM edge_observation_outbox WHERE event_id=? AND sequence>? ORDER BY sequence LIMIT ?`, eventID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []EdgeJournalItem{}
	for rows.Next() {
		var item EdgeJournalItem
		var payload string
		if err := rows.Scan(&item.Sequence, &item.ObservationID, &payload, &item.State); err != nil {
			return nil, err
		}
		item.Payload = json.RawMessage(payload)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) EdgePendingCount(ctx context.Context, eventID string) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM edge_observation_outbox WHERE event_id=? AND state IN ('pending','sent','rejected')`, eventID).Scan(&count)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return count, err
}
