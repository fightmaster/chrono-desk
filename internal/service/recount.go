// Package service hosts use cases on top of the engine and the store.
package service

import (
	"context"
	"fmt"
	"log"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/processor"
)

// RecountStats summarizes one recount run.
type RecountStats struct {
	LogsReplayed int `json:"logs_replayed"`
}

// Recounter wipes derived data and replays rfid logs through the engine —
// the desktop counterpart of run5's RecountRfid.
type Recounter struct {
	store *sqlite.Store
	proc  *processor.Processor
}

func NewRecounter(store *sqlite.Store, logger *log.Logger, debug bool) *Recounter {
	return &Recounter{
		store: store,
		proc:  processor.New(sqlite.NewProcessorRepo(store), logger, debug),
	}
}

// Recount reprocesses the whole event, or a single race when raceID != "".
func (r *Recounter) Recount(ctx context.Context, eventID, raceID string) (RecountStats, error) {
	if err := r.store.WipeDerivedResults(ctx, eventID, raceID); err != nil {
		return RecountStats{}, err
	}

	logs, err := r.store.ListRfidLogs(ctx, eventID)
	if err != nil {
		return RecountStats{}, err
	}

	for _, logEntry := range logs {
		if err := r.proc.Process(ctx, logEntry, raceID); err != nil {
			return RecountStats{}, fmt.Errorf("process log %s: %w", logEntry.ID, err)
		}
	}

	// Manual judge entries are authoritative: re-apply them on top of the
	// replayed chip data. First-wins, mirroring chip finishes (run5's PushResult
	// sets finish only while it is still null): apply only the FIRST manual entry
	// per member (ListManualResults orders by time_ms, id); any later duplicate is
	// left in the table but not counted.
	manual, err := r.store.ListManualResults(ctx, eventID, raceID)
	if err != nil {
		return RecountStats{}, err
	}
	appliedManual := make(map[string]bool, len(manual))
	for _, m := range manual {
		if appliedManual[m.MemberID] {
			continue
		}
		appliedManual[m.MemberID] = true
		if err := applyManualFinish(ctx, r.store, m.MemberID, m.TimeMs); err != nil {
			return RecountStats{}, err
		}
	}

	return RecountStats{LogsReplayed: len(logs)}, nil
}
