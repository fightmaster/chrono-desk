package sqlite

import (
	"database/sql"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

const rfidLogColumns = `id, event_id, status, number, time_ms, ant, epc, rssi, board, disabled_at,
	observation_version, capture_source_id, origin_system, origin_instance_id, origin_sequence,
	edge_version, source_session_id, identity_profile, clock_evidence_id, clock_quality`

const rfidLogPlaceholders = `?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?`

func rfidLogValues(l domain.RfidLog) []any {
	return append([]any{l.ID, l.EventID, l.Status, l.Number, l.TimeMs, l.Ant, l.EPC, l.RSSI, l.Board, l.DisabledAt}, observationMetadataValues(l)...)
}

func observationMetadataValues(l domain.RfidLog) []any {
	values := []any{nullablePositiveInt(l.ObservationVersion), nullableString(l.CaptureSourceID), nullableString(l.OriginSystem),
		nullableString(l.OriginInstanceID), nullablePositiveInt64(l.OriginSequence)}
	if l.EdgeMetadata == nil {
		return append(values, nil, nil, nil, nil, nil)
	}
	return append(values, l.EdgeVersion, l.SourceSessionID, l.IdentityProfile, l.ClockEvidenceID, l.ClockQuality)
}

func scanRfidLog(row interface{ Scan(...any) error }) (domain.RfidLog, error) {
	var l domain.RfidLog
	var version, sequence, edgeVersion sql.NullInt64
	var capture, origin, instance, session, profile, evidence, quality sql.NullString
	err := row.Scan(&l.ID, &l.EventID, &l.Status, &l.Number, &l.TimeMs, &l.Ant, &l.EPC, &l.RSSI, &l.Board, &l.DisabledAt,
		&version, &capture, &origin, &instance, &sequence, &edgeVersion, &session, &profile, &evidence, &quality)
	if err != nil {
		return domain.RfidLog{}, err
	}
	l.ObservationVersion, l.OriginSequence = int(version.Int64), sequence.Int64
	l.CaptureSourceID, l.OriginSystem, l.OriginInstanceID = capture.String, origin.String, instance.String
	if edgeVersion.Valid || session.Valid || profile.Valid || evidence.Valid || quality.Valid {
		l.EdgeMetadata = &domain.EdgeMetadata{EdgeVersion: int(edgeVersion.Int64), SourceSessionID: session.String,
			IdentityProfile: profile.String, ClockEvidenceID: evidence.String, ClockQuality: quality.String}
	}
	return l, nil
}
