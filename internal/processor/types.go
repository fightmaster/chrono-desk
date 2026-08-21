// Package processor derives race results from raw rfid logs. It is a port of
// rfid-sync's internal/syncer/processor adapted to string ids and unix-millis
// times; the matching semantics must stay identical to the reference
// implementation (and to run5's PushResult).
package processor

import "gitlab.com/fightmaster1/chrono-desk/internal/domain"

// Member is the engine's view of a participant.
type Member struct {
	ID                 string
	RaceID             string
	Number             *int64
	StartTimeMs        *int64
	StartTimeSource    domain.StartTimeSource
	StartObservationID string
	FinishTimeMs       *int64
	RaceStartedAtMs    *int64
}

// Checkpoint is the engine's view of a checkpoint bound to a board.
type Checkpoint struct {
	ID                 string
	Sort               int64
	Type               domain.CheckpointType
	SinceMs            *int64
	SinceOffsetSeconds *int64
	// SleepAfterPrevSeconds, when set, blocks this checkpoint until the given
	// number of seconds has elapsed since the member's most recent result
	// (отсечка). It is an additional AND constraint on top of Since /
	// SinceOffsetSeconds and is skipped when the member has no previous result.
	SleepAfterPrevSeconds *int64
}

// LastResult describes the member's most recent recorded result, ordered by
// result time. Both fields are nil when the member has no results yet.
type LastResult struct {
	Sort   *int64
	TimeMs *int64
}
