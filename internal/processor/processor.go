package processor

import (
	"context"
	"log"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	timing "gitlab.com/fightmaster1/timing-core"
)

type Processor struct {
	repo   Repository
	logger *log.Logger
	debug  bool
}

func New(repo Repository, logger *log.Logger, debug bool) *Processor {
	return &Processor{
		repo:   repo,
		logger: logger,
		debug:  debug,
	}
}

// Process derives at most one result from the given rfid log. raceFilter, when
// non-empty, skips members outside that race (run5's PushResult race guard);
// pass "" to process for any race. The log itself must already be persisted.
func (p *Processor) Process(ctx context.Context, logEntry domain.RfidLog, raceFilter string) error {
	if logEntry.TimeMs <= 0 {
		return nil
	}
	if !hasParticipantKey(logEntry) {
		p.debugLog("skip_push_missing_participant_key", "rfid_log_id=%s", logEntry.ID)
		return nil
	}

	eventTimeMs := logEntry.TimeMs
	stored := false
	memberID := ""
	if err := p.repo.WithTx(ctx, func(tx TxRepository) (bool, error) {
		exists, err := tx.ResultExists(ctx, logEntry.ID)
		if err != nil || exists {
			return false, err
		}

		disabled, err := tx.RfidLogDisabled(ctx, logEntry.ID)
		if err != nil {
			return false, err
		}
		if disabled {
			p.debugLog("skip_push_disabled_rfid_log", "rfid_log_id=%s", logEntry.ID)
			return false, nil
		}

		member, found, err := tx.LoadMember(ctx, logEntry.EventID, logEntry)
		if err != nil || !found {
			return false, err
		}
		if raceFilter != "" && member.RaceID != raceFilter {
			return false, nil
		}
		memberID = member.ID

		lastResult, err := tx.LoadLastResult(ctx, member.RaceID, member.ID)
		if err != nil {
			return false, err
		}
		passed, err := tx.LoadPassedCheckpoints(ctx, member.RaceID, member.ID)
		if err != nil {
			return false, err
		}
		checkpoints, err := tx.LoadCheckpoints(ctx, member.RaceID, logEntry.Board)
		if err != nil || len(checkpoints) == 0 {
			return false, err
		}
		checkpoint, eligible := selectCheckpoint(logEntry, member, lastResult, passed, checkpoints)
		if !eligible {
			return false, nil
		}

		inserted, err := tx.InsertResult(ctx, logEntry, member, checkpoint)
		if err != nil {
			return false, err
		}
		if !inserted {
			return false, nil
		}

		if err := tx.UpdateMemberTimes(ctx, member, checkpoint, eventTimeMs); err != nil {
			return false, err
		}

		stored = true
		return true, nil
	}); err != nil {
		return err
	}
	if !stored {
		p.debugLog("result_exists", "rfid_log_id=%s", logEntry.ID)
		return nil
	}
	p.debugLog("result_stored", "member_id=%s result_time_ms=%d", memberID, eventTimeMs)

	return nil
}

func selectCheckpoint(
	logEntry domain.RfidLog,
	member Member,
	last LastResult,
	passed map[string]bool,
	checkpoints []Checkpoint,
) (Checkpoint, bool) {
	coreCheckpoints := make([]timing.Checkpoint[string], 0, len(checkpoints))
	for _, checkpoint := range checkpoints {
		coreCheckpoints = append(coreCheckpoints, timing.Checkpoint[string]{
			ID: checkpoint.ID, Sort: checkpoint.Sort, Type: int(checkpoint.Type),
			SinceMs: checkpoint.SinceMs, SinceOffsetSeconds: checkpoint.SinceOffsetSeconds,
			SleepAfterPrevSeconds: checkpoint.SleepAfterPrevSeconds,
		})
	}
	index, ok := timing.SelectCheckpoint(
		timing.Observation{
			ID: logEntry.ID, TimeMs: logEntry.TimeMs, Number: logEntry.Number,
			EPC: logEntry.EPC, Board: logEntry.Board,
		},
		timing.Member[string]{
			ID: member.ID, RaceID: member.RaceID, StartTimeMs: member.StartTimeMs,
			RaceStartedAtMs: member.RaceStartedAtMs,
		},
		timing.LastResult{Sort: last.Sort, TimeMs: last.TimeMs},
		passed,
		coreCheckpoints,
	)
	if !ok {
		return Checkpoint{}, false
	}
	return checkpoints[index], true
}

func (p *Processor) debugLog(name, format string, args ...interface{}) {
	if p.debug && p.logger != nil {
		p.logger.Printf("event=%s "+format, append([]interface{}{name}, args...)...)
	}
}

func hasParticipantKey(logEntry domain.RfidLog) bool {
	return logEntry.Number > 0 || logEntry.EPC != ""
}
