package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/rfid-core/edge"
	"gitlab.com/fightmaster1/rfid-core/ingest"
)

const insertImportedRfidSQL = `INSERT INTO rfid_logs (` + rfidLogColumns + `) VALUES (` + rfidLogPlaceholders + `) ON CONFLICT(id) DO NOTHING`

// importObservation is shared by snapshot and feed imports. It joins the
// caller's transaction and never creates either kind of outbound journal.
func (s *Store) importObservation(ctx context.Context, incoming domain.RfidLog, insert *sql.Stmt) (ObservationFeedMutation, error) {
	if s.tx == nil {
		return ObservationFeedMutation{}, errors.New("observation import requires a transaction")
	}
	if err := incoming.ValidateEdgeMetadata(); err != nil {
		return ObservationFeedMutation{}, err
	}
	existing, found, err := s.findRfidLog(ctx, incoming.ID)
	if err == nil && !found {
		existing, found, err = s.findPhysicalRfidLog(ctx, incoming)
	}
	if err != nil {
		return ObservationFeedMutation{}, err
	}
	if !found {
		result, err := insert.ExecContext(ctx, rfidLogValues(incoming)...)
		if err != nil {
			return ObservationFeedMutation{}, fmt.Errorf("insert imported observation %s: %w", incoming.ID, err)
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return ObservationFeedMutation{}, errors.New("observation changed during import; retry validation")
		}
		return ObservationFeedMutation{Observation: incoming, Kind: ObservationFeedInserted}, nil
	}
	if err := existing.ValidateEdgeMetadata(); err != nil {
		return ObservationFeedMutation{}, err
	}
	edgeDuplicate := existing.EdgeMetadata != nil || incoming.EdgeMetadata != nil
	if (!edgeDuplicate && !sameImmutableObservation(existing, incoming)) || (edgeDuplicate && !samePhysicalObservation(existing, incoming)) {
		return ObservationFeedMutation{}, fmt.Errorf("observation %s conflicts with immutable local raw data", incoming.ID)
	}
	if !edgeDuplicate && !compatibleOrigin(existing, incoming) {
		return ObservationFeedMutation{}, fmt.Errorf("observation %s conflicts with local origin metadata", incoming.ID)
	}
	merged := existing
	// Preserve a different first writer and historical identity. Never attach an
	// incoming identity profile to another ID, or combine two source envelopes.
	if existing.ID == incoming.ID && existing.EdgeMetadata == nil && compatibleOrigin(existing, incoming) {
		if merged.ObservationVersion == 0 {
			merged.ObservationVersion = incoming.ObservationVersion
		}
		if merged.CaptureSourceID == "" {
			merged.CaptureSourceID = incoming.CaptureSourceID
		}
		if merged.OriginSystem == "" {
			merged.OriginSystem = incoming.OriginSystem
		}
		if merged.OriginInstanceID == "" {
			merged.OriginInstanceID = incoming.OriginInstanceID
		}
		if merged.OriginSequence == 0 {
			merged.OriginSequence = incoming.OriginSequence
		}
		merged.EdgeMetadata = incoming.EdgeMetadata
		if err := merged.ValidateEdgeMetadata(); err != nil {
			// A native first writer can retain case-preserved EPC/status that is
			// physically equivalent but not an edge envelope. Do not rewrite it
			// to make the incoming metadata fit; keep its complete original row.
			merged = existing
		}
	}
	if err := merged.ValidateEdgeMetadata(); err != nil {
		return ObservationFeedMutation{}, err
	}
	kind := ObservationFeedDuplicate
	if !sameNullableInt64(existing.DisabledAt, incoming.DisabledAt) {
		kind = ObservationFeedStateChanged
	}
	merged.DisabledAt = incoming.DisabledAt
	metadataChanged := existing.ObservationVersion != merged.ObservationVersion || existing.CaptureSourceID != merged.CaptureSourceID ||
		existing.OriginSystem != merged.OriginSystem || existing.OriginInstanceID != merged.OriginInstanceID || existing.OriginSequence != merged.OriginSequence ||
		existing.EdgeMetadata != merged.EdgeMetadata
	if metadataChanged || kind == ObservationFeedStateChanged {
		values := append([]any{merged.DisabledAt}, observationMetadataValues(merged)...)
		values = append(values, merged.ID)
		if _, err := s.db.ExecContext(ctx, `UPDATE rfid_logs SET disabled_at=?, observation_version=?,capture_source_id=?,origin_system=?,origin_instance_id=?,origin_sequence=?,edge_version=?,source_session_id=?,identity_profile=?,clock_evidence_id=?,clock_quality=? WHERE id=?`, values...); err != nil {
			return ObservationFeedMutation{}, fmt.Errorf("update imported observation %s: %w", merged.ID, err)
		}
	}
	return ObservationFeedMutation{Observation: merged, Kind: kind}, nil
}

func samePhysicalObservation(a, b domain.RfidLog) bool {
	// IDs may differ only when the bounded physical lookup found a historical
	// alias. Keep the existing ID/profile; compare the facts through shared core.
	return a.EventID == b.EventID && edge.SamePhysicalRead(
		ingest.Event{ID: b.ID, Board: a.Board, EPC: a.EPC, Time: a.TimeMs, Ant: a.Ant, Number: a.Number},
		ingest.Event{ID: b.ID, Board: b.Board, EPC: b.EPC, Time: b.TimeMs, Ant: b.Ant, Number: b.Number})
}

func (s *Store) findPhysicalRfidLog(ctx context.Context, log domain.RfidLog) (domain.RfidLog, bool, error) {
	// Native site history can arrive after its edge counterpart under an old
	// plate ID. Alias in either order only when an edge row participates; do not
	// change native-only identity rules. Any edge marker counts, so incomplete
	// or future metadata is validated below rather than downgraded to native.
	// Edge-first ordering prevents LIMIT 2 from hiding an edge among native
	// aliases. The existing physical index bounds the candidate lookup.
	rows, err := s.db.QueryContext(ctx, `SELECT id,
		(edge_version IS NOT NULL OR source_session_id IS NOT NULL OR identity_profile IS NOT NULL OR clock_evidence_id IS NOT NULL OR clock_quality IS NOT NULL) AS has_edge
		FROM rfid_logs WHERE event_id=? AND board=? AND time_ms=? AND ant=? AND epc=? COLLATE NOCASE AND number=?
		ORDER BY has_edge DESC, id LIMIT 2`, log.EventID, log.Board, log.TimeMs, log.Ant, log.EPC, log.Number)
	if err != nil {
		return domain.RfidLog{}, false, err
	}
	var ids []string
	var edgeFound bool
	for rows.Next() {
		var id string
		var hasEdge bool
		if err := rows.Scan(&id, &hasEdge); err != nil {
			_ = rows.Close()
			return domain.RfidLog{}, false, err
		}
		ids = append(ids, id)
		edgeFound = edgeFound || hasEdge
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return domain.RfidLog{}, false, err
	}
	if log.EdgeMetadata == nil && !edgeFound {
		return domain.RfidLog{}, false, nil
	}
	if len(ids) > 1 {
		return domain.RfidLog{}, false, errors.New("ambiguous legacy physical observation")
	}
	if len(ids) == 0 {
		return domain.RfidLog{}, false, nil
	}
	return s.findRfidLog(ctx, ids[0])
}
