package processor

import (
	"context"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

// Repository owns the transaction boundary for one member progression
// transition. All selection reads must use the TxRepository passed to WithTx.
type Repository interface {
	WithTx(ctx context.Context, fn func(tx TxRepository) (bool, error)) error
}

// TxRepository is the complete read/write surface for one atomic progression
// decision. Reading through the root connection before this boundary is
// forbidden: two observations could otherwise select the same once checkpoint.
type TxRepository interface {
	ResultExists(ctx context.Context, rfidLogID string) (bool, error)
	RfidLogDisabled(ctx context.Context, rfidLogID string) (bool, error)
	// LoadMember resolves the participant by number (when > 0) or EPC.
	// found=false (and no error) when nobody matches.
	LoadMember(ctx context.Context, eventID string, log domain.RfidLog) (member Member, found bool, err error)
	LoadLastResult(ctx context.Context, raceID, memberID string) (LastResult, error)
	LoadPassedCheckpoints(ctx context.Context, raceID, memberID string) (map[string]bool, error)
	LoadCheckpoints(ctx context.Context, raceID, board string) ([]Checkpoint, error)
	// InsertResult stores the derived result; returns false (no error) when a
	// result for this rfid log already exists.
	InsertResult(ctx context.Context, log domain.RfidLog, member Member, checkpoint Checkpoint) (bool, error)
	UpdateMemberTimes(ctx context.Context, member Member, checkpoint Checkpoint, eventTimeMs int64) error
}
