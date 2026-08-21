package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ProjectionEvidenceCheck records one exact/revision comparison performed by
// planned recount execution.
type ProjectionEvidenceCheck struct {
	ExactChanged    bool
	RevisionChanged bool
	VersionMismatch bool
	CheckedAtMs     int64
}

// ProjectionEvidenceParity is durable field-acceptance telemetry. Matches
// include both stable/stable and stale/stale comparisons.
type ProjectionEvidenceParity struct {
	RevisionVersion        string `json:"revision_version"`
	Checks                 int64  `json:"checks"`
	Matches                int64  `json:"matches"`
	Mismatches             int64  `json:"mismatches"`
	HashOnlyMismatches     int64  `json:"hash_only_mismatches"`
	RevisionOnlyMismatches int64  `json:"revision_only_mismatches"`
	VersionMismatches      int64  `json:"version_mismatches"`
	LastCheckedAtMs        int64  `json:"last_checked_at_ms"`
	LastMismatchAtMs       *int64 `json:"last_mismatch_at_ms"`
}

func (s *Store) RecordProjectionEvidenceCheck(ctx context.Context, eventID string, check ProjectionEvidenceCheck) error {
	mismatch := check.VersionMismatch || check.ExactChanged != check.RevisionChanged
	matchCount := boolInt(!mismatch)
	mismatchCount := boolInt(mismatch)
	hashOnly := boolInt(!check.VersionMismatch && check.ExactChanged && !check.RevisionChanged)
	revisionOnly := boolInt(!check.VersionMismatch && !check.ExactChanged && check.RevisionChanged)
	versionMismatch := boolInt(check.VersionMismatch)
	var lastMismatch any
	if mismatch {
		lastMismatch = check.CheckedAtMs
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO projection_evidence_parity (
			event_id, revision_version, checks, matches, mismatches,
			hash_only_mismatches, revision_only_mismatches, version_mismatches,
			last_checked_at_ms, last_mismatch_at_ms)
		VALUES (?, ?, 1, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(event_id) DO UPDATE SET
			revision_version=excluded.revision_version,
			checks=projection_evidence_parity.checks+1,
			matches=projection_evidence_parity.matches+excluded.matches,
			mismatches=projection_evidence_parity.mismatches+excluded.mismatches,
			hash_only_mismatches=projection_evidence_parity.hash_only_mismatches+excluded.hash_only_mismatches,
			revision_only_mismatches=projection_evidence_parity.revision_only_mismatches+excluded.revision_only_mismatches,
			version_mismatches=projection_evidence_parity.version_mismatches+excluded.version_mismatches,
			last_checked_at_ms=MAX(projection_evidence_parity.last_checked_at_ms, excluded.last_checked_at_ms),
			last_mismatch_at_ms=CASE
				WHEN excluded.last_mismatch_at_ms IS NULL THEN projection_evidence_parity.last_mismatch_at_ms
				ELSE MAX(COALESCE(projection_evidence_parity.last_mismatch_at_ms, 0), excluded.last_mismatch_at_ms)
			END`,
		eventID, ProjectionRevisionVersion, matchCount, mismatchCount, hashOnly,
		revisionOnly, versionMismatch, check.CheckedAtMs, lastMismatch,
	)
	if err != nil {
		return fmt.Errorf("record projection evidence parity: %w", err)
	}
	return nil
}

func (s *Store) GetProjectionEvidenceParity(ctx context.Context, eventID string) (ProjectionEvidenceParity, error) {
	parity := ProjectionEvidenceParity{RevisionVersion: ProjectionRevisionVersion}
	err := s.db.QueryRowContext(ctx, `
		SELECT revision_version, checks, matches, mismatches, hash_only_mismatches,
			revision_only_mismatches, version_mismatches, last_checked_at_ms, last_mismatch_at_ms
		FROM projection_evidence_parity WHERE event_id=?`, eventID).Scan(
		&parity.RevisionVersion, &parity.Checks, &parity.Matches, &parity.Mismatches,
		&parity.HashOnlyMismatches, &parity.RevisionOnlyMismatches,
		&parity.VersionMismatches, &parity.LastCheckedAtMs, &parity.LastMismatchAtMs,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return parity, nil
	}
	if err != nil {
		return ProjectionEvidenceParity{}, fmt.Errorf("read projection evidence parity: %w", err)
	}
	return parity, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
