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

	return RecountStats{LogsReplayed: len(logs)}, nil
}
