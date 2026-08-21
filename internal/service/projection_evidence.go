package service

import (
	"fmt"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/version"
)

const projectionEvidenceAcceptanceWindow = "projection-revision-v1-field-2026-08-21"

// CurrentProjectionEvidenceIdentity pins operational evidence to one revision
// contract, deliberate field-acceptance window and reproducible application
// build. Starting a new window is an explicit code and documentation change.
func CurrentProjectionEvidenceIdentity() sqlite.ProjectionEvidenceIdentity {
	return sqlite.ProjectionEvidenceIdentity{
		RevisionVersion:  sqlite.ProjectionRevisionVersion,
		AcceptanceWindow: projectionEvidenceAcceptanceWindow,
		AppBuild:         fmt.Sprintf("%s+%s@%s", version.Semver, version.Build, version.Commit),
	}
}
