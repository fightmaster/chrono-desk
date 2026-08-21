package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ProjectionEvidenceIdentity prevents acceptance samples from different
// revision contracts, rollout windows or application builds being combined.
type ProjectionEvidenceIdentity struct {
	RevisionVersion  string `json:"revision_version"`
	AcceptanceWindow string `json:"acceptance_window"`
	AppBuild         string `json:"app_build"`
}

// ProjectionEvidenceCheck records one exact/revision comparison performed by
// planned recount execution.
type ProjectionEvidenceCheck struct {
	ExactChanged    bool
	RevisionChanged bool
	VersionMismatch bool
	CheckedAtMs     int64
}

// ProjectionEvidenceFailure records a comparison whose projection transaction
// rolled back. It is written after rollback and never counts as a committed
// parity check.
type ProjectionEvidenceFailure struct {
	ProjectionEvidenceCheck
	FailureClass string
	FailedAtMs   int64
}

// ProjectionEvidenceParity is durable field-acceptance telemetry. Matches
// include both stable/stable and stale/stale committed comparisons. Failed
// attempts are separate and always block an authority switch.
type ProjectionEvidenceParity struct {
	ProjectionEvidenceIdentity
	Attempts                   int64  `json:"attempts"`
	Checks                     int64  `json:"checks"`
	Matches                    int64  `json:"matches"`
	Mismatches                 int64  `json:"mismatches"`
	HashOnlyMismatches         int64  `json:"hash_only_mismatches"`
	RevisionOnlyMismatches     int64  `json:"revision_only_mismatches"`
	VersionMismatches          int64  `json:"version_mismatches"`
	ReplayFailures             int64  `json:"replay_failures"`
	FailedMismatchAttempts     int64  `json:"failed_mismatch_attempts"`
	LastAttemptAtMs            int64  `json:"last_attempt_at_ms"`
	LastCheckedAtMs            int64  `json:"last_checked_at_ms"`
	LastMismatchAtMs           *int64 `json:"last_mismatch_at_ms"`
	LastFailureAtMs            *int64 `json:"last_failure_at_ms"`
	LastFailureClass           string `json:"last_failure_class"`
	LastFailureExactChanged    bool   `json:"last_failure_exact_changed"`
	LastFailureRevisionChanged bool   `json:"last_failure_revision_changed"`
	LastFailureVersionMismatch bool   `json:"last_failure_version_mismatch"`
	AuthoritySwitchBlocked     bool   `json:"authority_switch_blocked"`
}

func (s *Store) RecordProjectionEvidenceCheck(ctx context.Context, eventID string, identity ProjectionEvidenceIdentity, check ProjectionEvidenceCheck) error {
	if err := validateProjectionEvidenceIdentity(identity); err != nil {
		return err
	}
	mismatch := projectionEvidenceMismatch(check)
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
		INSERT INTO projection_evidence_acceptance (
			event_id, revision_version, acceptance_window, app_build,
			attempts, checks, matches, mismatches, hash_only_mismatches,
			revision_only_mismatches, version_mismatches, last_attempt_at_ms,
			last_checked_at_ms, last_mismatch_at_ms)
		VALUES (?, ?, ?, ?, 1, 1, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(event_id, revision_version, acceptance_window, app_build) DO UPDATE SET
			attempts=projection_evidence_acceptance.attempts+1,
			checks=projection_evidence_acceptance.checks+1,
			matches=projection_evidence_acceptance.matches+excluded.matches,
			mismatches=projection_evidence_acceptance.mismatches+excluded.mismatches,
			hash_only_mismatches=projection_evidence_acceptance.hash_only_mismatches+excluded.hash_only_mismatches,
			revision_only_mismatches=projection_evidence_acceptance.revision_only_mismatches+excluded.revision_only_mismatches,
			version_mismatches=projection_evidence_acceptance.version_mismatches+excluded.version_mismatches,
			last_attempt_at_ms=MAX(projection_evidence_acceptance.last_attempt_at_ms, excluded.last_attempt_at_ms),
			last_checked_at_ms=MAX(projection_evidence_acceptance.last_checked_at_ms, excluded.last_checked_at_ms),
			last_mismatch_at_ms=CASE
				WHEN excluded.last_mismatch_at_ms IS NULL THEN projection_evidence_acceptance.last_mismatch_at_ms
				ELSE MAX(COALESCE(projection_evidence_acceptance.last_mismatch_at_ms, 0), excluded.last_mismatch_at_ms)
			END`,
		eventID, identity.RevisionVersion, identity.AcceptanceWindow, identity.AppBuild,
		matchCount, mismatchCount, hashOnly, revisionOnly, versionMismatch,
		check.CheckedAtMs, check.CheckedAtMs, lastMismatch,
	)
	if err != nil {
		return fmt.Errorf("record projection evidence parity: %w", err)
	}
	return nil
}

func (s *Store) RecordProjectionEvidenceFailure(ctx context.Context, eventID string, identity ProjectionEvidenceIdentity, failure ProjectionEvidenceFailure) error {
	if err := validateProjectionEvidenceIdentity(identity); err != nil {
		return err
	}
	if failure.FailureClass == "" {
		return errors.New("projection evidence failure class is required")
	}
	mismatch := projectionEvidenceMismatch(failure.ProjectionEvidenceCheck)
	var lastMismatch any
	if mismatch {
		lastMismatch = failure.CheckedAtMs
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO projection_evidence_acceptance (
			event_id, revision_version, acceptance_window, app_build,
			attempts, replay_failures, failed_mismatch_attempts,
			last_attempt_at_ms, last_mismatch_at_ms, last_failure_at_ms,
			last_failure_class, last_failure_exact_changed,
			last_failure_revision_changed, last_failure_version_mismatch)
		VALUES (?, ?, ?, ?, 1, 1, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(event_id, revision_version, acceptance_window, app_build) DO UPDATE SET
			attempts=projection_evidence_acceptance.attempts+1,
			replay_failures=projection_evidence_acceptance.replay_failures+1,
			failed_mismatch_attempts=projection_evidence_acceptance.failed_mismatch_attempts+excluded.failed_mismatch_attempts,
			last_attempt_at_ms=MAX(projection_evidence_acceptance.last_attempt_at_ms, excluded.last_attempt_at_ms),
			last_mismatch_at_ms=CASE
				WHEN excluded.last_mismatch_at_ms IS NULL THEN projection_evidence_acceptance.last_mismatch_at_ms
				ELSE MAX(COALESCE(projection_evidence_acceptance.last_mismatch_at_ms, 0), excluded.last_mismatch_at_ms)
			END,
			last_failure_at_ms=excluded.last_failure_at_ms,
			last_failure_class=excluded.last_failure_class,
			last_failure_exact_changed=excluded.last_failure_exact_changed,
			last_failure_revision_changed=excluded.last_failure_revision_changed,
			last_failure_version_mismatch=excluded.last_failure_version_mismatch`,
		eventID, identity.RevisionVersion, identity.AcceptanceWindow, identity.AppBuild,
		boolInt(mismatch), failure.CheckedAtMs, lastMismatch, failure.FailedAtMs,
		failure.FailureClass, failure.ExactChanged, failure.RevisionChanged, failure.VersionMismatch,
	)
	if err != nil {
		return fmt.Errorf("record projection evidence failure: %w", err)
	}
	return nil
}

func (s *Store) GetProjectionEvidenceParity(ctx context.Context, eventID string, identity ProjectionEvidenceIdentity) (ProjectionEvidenceParity, error) {
	if err := validateProjectionEvidenceIdentity(identity); err != nil {
		return ProjectionEvidenceParity{}, err
	}
	parity := ProjectionEvidenceParity{ProjectionEvidenceIdentity: identity}
	err := scanProjectionEvidenceParity(s.db.QueryRowContext(ctx, projectionEvidenceSelect+`
		WHERE event_id=? AND revision_version=? AND acceptance_window=? AND app_build=?`,
		eventID, identity.RevisionVersion, identity.AcceptanceWindow, identity.AppBuild), &parity)
	if errors.Is(err, sql.ErrNoRows) {
		return parity, nil
	}
	if err != nil {
		return ProjectionEvidenceParity{}, fmt.Errorf("read projection evidence parity: %w", err)
	}
	setProjectionAuthorityBlock(&parity)
	return parity, nil
}

func (s *Store) ListProjectionEvidenceParity(ctx context.Context, eventID string) ([]ProjectionEvidenceParity, error) {
	rows, err := s.db.QueryContext(ctx, projectionEvidenceSelect+`
		WHERE event_id=? ORDER BY acceptance_window, revision_version, app_build`, eventID)
	if err != nil {
		return nil, fmt.Errorf("list projection evidence parity: %w", err)
	}
	defer rows.Close()

	result := make([]ProjectionEvidenceParity, 0)
	for rows.Next() {
		var parity ProjectionEvidenceParity
		if err := scanProjectionEvidenceParity(rows, &parity); err != nil {
			return nil, fmt.Errorf("scan projection evidence parity: %w", err)
		}
		setProjectionAuthorityBlock(&parity)
		result = append(result, parity)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate projection evidence parity: %w", err)
	}
	return result, nil
}

const projectionEvidenceSelect = `
	SELECT revision_version, acceptance_window, app_build, attempts, checks,
		matches, mismatches, hash_only_mismatches, revision_only_mismatches,
		version_mismatches, replay_failures, failed_mismatch_attempts,
		last_attempt_at_ms, last_checked_at_ms, last_mismatch_at_ms,
		last_failure_at_ms, last_failure_class, last_failure_exact_changed,
		last_failure_revision_changed, last_failure_version_mismatch
	FROM projection_evidence_acceptance `

type projectionEvidenceScanner interface {
	Scan(dest ...any) error
}

func scanProjectionEvidenceParity(scanner projectionEvidenceScanner, parity *ProjectionEvidenceParity) error {
	return scanner.Scan(
		&parity.RevisionVersion, &parity.AcceptanceWindow, &parity.AppBuild,
		&parity.Attempts, &parity.Checks, &parity.Matches, &parity.Mismatches,
		&parity.HashOnlyMismatches, &parity.RevisionOnlyMismatches,
		&parity.VersionMismatches, &parity.ReplayFailures, &parity.FailedMismatchAttempts,
		&parity.LastAttemptAtMs, &parity.LastCheckedAtMs, &parity.LastMismatchAtMs,
		&parity.LastFailureAtMs, &parity.LastFailureClass,
		&parity.LastFailureExactChanged, &parity.LastFailureRevisionChanged,
		&parity.LastFailureVersionMismatch,
	)
}

func validateProjectionEvidenceIdentity(identity ProjectionEvidenceIdentity) error {
	if identity.RevisionVersion == "" || identity.AcceptanceWindow == "" || identity.AppBuild == "" {
		return errors.New("projection evidence identity is incomplete")
	}
	return nil
}

func projectionEvidenceMismatch(check ProjectionEvidenceCheck) bool {
	return check.VersionMismatch || check.ExactChanged != check.RevisionChanged
}

func setProjectionAuthorityBlock(parity *ProjectionEvidenceParity) {
	parity.AuthoritySwitchBlocked = parity.Mismatches > 0 || parity.ReplayFailures > 0
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
