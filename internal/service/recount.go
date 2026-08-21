// Package service hosts use cases on top of the engine and the store.
package service

import (
	"context"
	"fmt"
	"log"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/processor"
	timing "gitlab.com/fightmaster1/timing-core"
)

const maxTargetedMembersPerPlan = 500

// RecountStats summarizes one recount run.
type RecountStats struct {
	LogsReplayed             int  `json:"logs_replayed"`
	MembersReplayed          int  `json:"members_replayed"`
	RacesReplayed            int  `json:"races_replayed"`
	EventReplayed            bool `json:"event_replayed"`
	EvidenceFallback         bool `json:"evidence_fallback"`
	RevisionEvidenceChecked  bool `json:"revision_evidence_checked"`
	RevisionEvidenceMismatch bool `json:"revision_evidence_mismatch"`
}

// Recounter wipes derived data and replays rfid logs through the engine —
// the desktop counterpart of run5's RecountRfid.
type Recounter struct {
	store  recountStore
	logger *log.Logger
	debug  bool
}

func NewRecounter(store *sqlite.Store, logger *log.Logger, debug bool) *Recounter {
	return &Recounter{
		store: newSQLiteRecountStore(store), logger: logger, debug: debug,
	}
}

// Recount reprocesses the whole event, or a single race when raceID != "".
func (r *Recounter) Recount(ctx context.Context, eventID, raceID string) (RecountStats, error) {
	var stats RecountStats
	err := r.store.WithinTx(ctx, func(txStore recountTxStore) error {
		var err error
		stats, err = r.recount(ctx, txStore, eventID, raceID)
		if raceID == "" {
			stats.EventReplayed = true
		} else {
			stats.RacesReplayed = 1
		}
		return err
	})
	return stats, err
}

// RecountPlan executes a dual-evidence-bound plan inside one SQLite write
// transaction. Any exact/revision disagreement fails closed to event replay.
func (r *Recounter) RecountPlan(ctx context.Context, eventID string, plan timing.ProjectionPlan[string, string], expected sqlite.ProjectionFenceEvidence) (RecountStats, bool, error) {
	// A large targeted IN-list is slower and eventually reaches SQLite's bind
	// variable limit. Multiple race replays also rescan the event log once per
	// race. In both cases one event replay is the bounded operation.
	replayEvent := plan.ReplayEvent || len(plan.Races) > 1 || len(plan.Members) > maxTargetedMembersPerPlan
	hasActions := replayEvent || len(plan.Races) > 0 || len(plan.Members) > 0
	hasEvidence := plan.ConfigVersion != "" && plan.InputWatermark != ""
	if !hasActions && !hasEvidence {
		return RecountStats{}, false, nil
	}
	var stats RecountStats
	err := r.store.WithinTx(ctx, func(store recountTxStore) error {
		actual, err := store.ProjectionFenceEvidence(ctx, eventID)
		if err != nil {
			return err
		}
		exactChanged := !plan.EvidenceValid || actual.Exact.ConfigVersion != plan.ConfigVersion || actual.Exact.InputWatermark != plan.InputWatermark
		revisionChanged := actual.Revisions != expected.Revisions
		versionMismatch := actual.RevisionVersion != expected.RevisionVersion
		revisionMismatch := plan.EvidenceValid && (versionMismatch || exactChanged != revisionChanged)
		if plan.EvidenceValid {
			if err := store.RecordProjectionEvidenceCheck(ctx, eventID, sqlite.ProjectionEvidenceCheck{
				ExactChanged: exactChanged, RevisionChanged: revisionChanged,
				VersionMismatch: versionMismatch, CheckedAtMs: time.Now().UnixMilli(),
			}); err != nil {
				return err
			}
		}
		if revisionMismatch {
			if r.logger != nil {
				r.logger.Printf("projection evidence parity mismatch event=%s exact_changed=%t revision_changed=%t version_mismatch=%t", eventID, exactChanged, revisionChanged, versionMismatch)
			}
		}
		if exactChanged || revisionChanged || versionMismatch {
			stats, err = r.recount(ctx, store, eventID, "")
			stats.EventReplayed = true
			stats.EvidenceFallback = true
			stats.RevisionEvidenceChecked = true
			stats.RevisionEvidenceMismatch = revisionMismatch
			if err != nil {
				return err
			}
			return store.ClearProjectionPending(ctx, eventID)
		}
		if replayEvent {
			stats, err = r.recount(ctx, store, eventID, "")
			stats.EventReplayed = true
			stats.RevisionEvidenceChecked = true
			stats.RevisionEvidenceMismatch = revisionMismatch
			if err != nil {
				return err
			}
			return store.ClearProjectionPending(ctx, eventID)
		}
		for _, race := range plan.Races {
			result, err := r.recount(ctx, store, eventID, race.RaceID)
			if err != nil {
				return err
			}
			stats.LogsReplayed += result.LogsReplayed
			stats.RacesReplayed++
		}
		if len(plan.Members) > 0 {
			memberIDs := make([]string, 0, len(plan.Members))
			for _, member := range plan.Members {
				memberIDs = append(memberIDs, member.MemberID)
			}
			result, err := r.recountMembers(ctx, store, eventID, memberIDs)
			if err != nil {
				return err
			}
			stats.LogsReplayed += result.LogsReplayed
			stats.MembersReplayed += len(memberIDs)
		}
		stats.RevisionEvidenceChecked = true
		stats.RevisionEvidenceMismatch = revisionMismatch
		return store.ClearProjectionPending(ctx, eventID)
	})
	return stats, hasActions, err
}

func (r *Recounter) recount(ctx context.Context, store recountTxStore, eventID, raceID string) (RecountStats, error) {
	if err := store.WipeDerivedResults(ctx, eventID, raceID); err != nil {
		return RecountStats{}, err
	}

	logs, err := store.ListRfidLogs(ctx, eventID)
	if err != nil {
		return RecountStats{}, err
	}

	if err := r.replayLogs(ctx, store, logs, raceID); err != nil {
		return RecountStats{}, err
	}

	// Manual judge entries are authoritative: re-apply them on top of the
	// replayed chip data. First-wins, mirroring chip finishes (run5's PushResult
	// sets finish only while it is still null): apply only the FIRST manual entry
	// per member (ListManualResults orders by time_ms, id); any later duplicate is
	// left in the table but not counted.
	manual, err := store.ListManualResults(ctx, eventID, raceID)
	if err != nil {
		return RecountStats{}, err
	}
	if err := r.reapplyManual(ctx, store, manual); err != nil {
		return RecountStats{}, err
	}

	return RecountStats{LogsReplayed: len(logs)}, nil
}

func (r *Recounter) recountMembers(ctx context.Context, store recountTxStore, eventID string, memberIDs []string) (RecountStats, error) {
	if err := store.WipeDerivedResultsForMembers(ctx, eventID, memberIDs); err != nil {
		return RecountStats{}, err
	}
	logs, err := store.ListRfidLogsForMembers(ctx, eventID, memberIDs)
	if err != nil {
		return RecountStats{}, err
	}
	if err := r.replayLogs(ctx, store, logs, ""); err != nil {
		return RecountStats{}, err
	}
	manual, err := store.ListManualResultsForMembers(ctx, eventID, memberIDs)
	if err != nil {
		return RecountStats{}, err
	}
	if err := r.reapplyManual(ctx, store, manual); err != nil {
		return RecountStats{}, err
	}
	return RecountStats{LogsReplayed: len(logs)}, nil
}

func (r *Recounter) replayLogs(ctx context.Context, store recountTxStore, logs []domain.RfidLog, raceID string) error {
	proc := processor.New(store.ProcessorRepository(), r.logger, r.debug)
	for _, logEntry := range logs {
		if err := proc.Process(ctx, logEntry, raceID); err != nil {
			return fmt.Errorf("process log %s: %w", logEntry.ID, err)
		}
	}
	return nil
}

func (r *Recounter) reapplyManual(ctx context.Context, store recountTxStore, manual []sqlite.ManualResult) error {
	applied := make(map[string]bool, len(manual))
	for _, result := range manual {
		if applied[result.MemberID] {
			continue
		}
		applied[result.MemberID] = true
		if err := applyManualFinish(ctx, store, result.MemberID, result.TimeMs); err != nil {
			return err
		}
	}
	return nil
}
