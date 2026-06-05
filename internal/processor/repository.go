package processor

import (
	"context"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

// Repository provides the engine's reads. Mirrors rfid-sync's interface minus
// board→event lookup (one event = one database) and rfid_log insertion
// (importers/ingest own that).
type Repository interface {
	ResultExists(ctx context.Context, rfidLogID string) (bool, error)
	RfidLogDisabled(ctx context.Context, rfidLogID string) (bool, error)
	// LoadMember resolves the participant by number (when > 0) or EPC.
	// found=false (and no error) when nobody matches.
	LoadMember(ctx context.Context, eventID string, log domain.RfidLog) (member Member, found bool, err error)
	LoadLastResult(ctx context.Context, raceID, memberID string) (LastResult, error)
	LoadPassedCheckpoints(ctx context.Context, raceID, memberID string) (map[string]bool, error)
	LoadCheckpoints(ctx context.Context, raceID, board string) ([]Checkpoint, error)
	WithTx(ctx context.Context, fn func(tx TxRepository) (bool, error)) error
}

// TxRepository is the transactional write surface used for one accepted pass.
type TxRepository interface {
	// InsertResult stores the derived result; returns false (no error) when a
	// result for this rfid log already exists.
	InsertResult(ctx context.Context, log domain.RfidLog, member Member, checkpoint Checkpoint) (bool, error)
	UpdateMemberTimes(ctx context.Context, member Member, checkpoint Checkpoint, eventTimeMs int64) error
}
