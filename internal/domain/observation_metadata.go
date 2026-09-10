package domain

import (
	"fmt"
	"strconv"
	"time"

	"gitlab.com/fightmaster1/rfid-core/edge"
	"gitlab.com/fightmaster1/rfid-core/ingest"
)

// EdgeMetadata is optional for legacy observations. Embedding a pointer in
// transport DTOs distinguishes absent fields from explicit zero/null markers.
type EdgeMetadata struct {
	EdgeVersion     int    `json:"edge_version"`
	SourceSessionID string `json:"source_session_id"`
	IdentityProfile string `json:"identity_profile"`
	ClockEvidenceID string `json:"clock_evidence_id"`
	ClockQuality    string `json:"clock_quality"`
}

// ValidateEdgeMetadata delegates wire semantics to the shared codec. This
// reconstructs only the persisted timestamp representation for validation;
// it must not be used to synthesize a relay payload or new clock evidence.
func (l RfidLog) ValidateEdgeMetadata() error {
	if l.EdgeMetadata == nil {
		return nil
	}
	eventID, err := strconv.ParseInt(l.EventID, 10, 64)
	if err != nil || strconv.FormatInt(eventID, 10) != l.EventID {
		return fmt.Errorf("edge observation %s: invalid event ID", l.ID)
	}
	err = edge.Validate(ingest.Event{
		ID: l.ID, ExternalEventID: eventID, Status: l.Status, Number: l.Number,
		Time: l.TimeMs, RTC: time.UnixMilli(l.TimeMs).UTC().Format(time.RFC3339Nano),
		Ant: l.Ant, EPC: l.EPC, RSSI: l.RSSI, Board: l.Board,
		ObservationVersion: l.ObservationVersion, CaptureSourceID: l.CaptureSourceID,
		OriginSystem: l.OriginSystem, OriginInstanceID: l.OriginInstanceID,
		OriginSequence: uint64(l.OriginSequence), EdgeVersion: l.EdgeVersion,
		SourceSessionID: l.SourceSessionID, IdentityProfile: l.IdentityProfile,
		ClockEvidenceID: l.ClockEvidenceID, ClockQuality: l.ClockQuality,
	})
	if err != nil {
		return fmt.Errorf("edge observation %s: %w", l.ID, err)
	}
	return nil
}
