package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/processor"
	timing "gitlab.com/fightmaster1/timing-core"
)

// ProcessorRepo implements processor.Repository over an event database.
// Queries mirror rfid-sync's mysqlrepo so the engine behaves identically.
type ProcessorRepo struct {
	store *Store
	db    database
}

func NewProcessorRepo(store *Store) *ProcessorRepo {
	return &ProcessorRepo{store: store, db: store.db}
}

func (r *ProcessorRepo) ResultExists(ctx context.Context, rfidLogID string) (bool, error) {
	var one int
	err := r.db.QueryRowContext(ctx,
		`SELECT 1 FROM results WHERE rfid_log_id = ? LIMIT 1`, rfidLogID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("result exists %s: %w", rfidLogID, err)
	}
	return true, nil
}

func (r *ProcessorRepo) RfidLogDisabled(ctx context.Context, rfidLogID string) (bool, error) {
	var disabledAt sql.NullInt64
	err := r.db.QueryRowContext(ctx,
		`SELECT disabled_at FROM rfid_logs WHERE id = ? LIMIT 1`, rfidLogID).Scan(&disabledAt)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("rfid log disabled %s: %w", rfidLogID, err)
	}
	return disabledAt.Valid, nil
}

const (
	memberByNumberSQL = `SELECT m.id, m.race_id, m.number, m.start_time_ms, m.finish_time_ms, r.started_at_ms
		FROM members m JOIN races r ON r.id = m.race_id
		WHERE m.event_id = ? AND m.number = ? LIMIT 1`
	memberByEPCSQL = `SELECT m.id, m.race_id, m.number, m.start_time_ms, m.finish_time_ms, r.started_at_ms
		FROM members m JOIN races r ON r.id = m.race_id
		WHERE m.event_id = ? AND m.epc = ? LIMIT 1`
)

// LoadMember resolves by number first, then EPC — same priority as rfid-sync.
func (r *ProcessorRepo) LoadMember(ctx context.Context, eventID string, logEntry domain.RfidLog) (processor.Member, bool, error) {
	query := memberByEPCSQL
	var arg any = logEntry.EPC
	if logEntry.Number > 0 {
		query = memberByNumberSQL
		arg = logEntry.Number
	}

	var m processor.Member
	var number, startMs, finishMs, raceStartedMs sql.NullInt64
	err := r.db.QueryRowContext(ctx, query, eventID, arg).
		Scan(&m.ID, &m.RaceID, &number, &startMs, &finishMs, &raceStartedMs)
	if err == sql.ErrNoRows {
		return processor.Member{}, false, nil
	}
	if err != nil {
		return processor.Member{}, false, fmt.Errorf("load member: %w", err)
	}
	m.Number = nullableInt64(number)
	m.StartTimeMs = nullableInt64(startMs)
	m.FinishTimeMs = nullableInt64(finishMs)
	m.RaceStartedAtMs = nullableInt64(raceStartedMs)
	return m, true, nil
}

func (r *ProcessorRepo) LoadLastResult(ctx context.Context, raceID, memberID string) (processor.LastResult, error) {
	var sort, timeMs sql.NullInt64
	err := r.db.QueryRowContext(ctx, `
		SELECT c.sort, res.time_ms
		FROM results res JOIN checkpoints c ON c.id = res.checkpoint_id
		WHERE res.race_id = ? AND res.member_id = ?
		ORDER BY res.time_ms DESC LIMIT 1`, raceID, memberID).
		Scan(&sort, &timeMs)
	if err == sql.ErrNoRows {
		return processor.LastResult{}, nil
	}
	if err != nil {
		return processor.LastResult{}, fmt.Errorf("load last result: %w", err)
	}
	return processor.LastResult{Sort: nullableInt64(sort), TimeMs: nullableInt64(timeMs)}, nil
}

func (r *ProcessorRepo) LoadPassedCheckpoints(ctx context.Context, raceID, memberID string) (map[string]bool, error) {
	// Manual judge entries (checkpoint_id IS NULL) are not checkpoint passes.
	rows, err := r.db.QueryContext(ctx,
		`SELECT checkpoint_id FROM results WHERE race_id = ? AND member_id = ? AND checkpoint_id IS NOT NULL`,
		raceID, memberID)
	if err != nil {
		return nil, fmt.Errorf("load passed checkpoints: %w", err)
	}
	defer rows.Close()

	passed := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		passed[id] = true
	}
	return passed, rows.Err()
}

func (r *ProcessorRepo) LoadCheckpoints(ctx context.Context, raceID, board string) ([]processor.Checkpoint, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, sort, type, since_ms, since_offset_seconds, sleep_after_prev_seconds
		FROM checkpoints WHERE race_id = ? AND board = ? ORDER BY sort`, raceID, board)
	if err != nil {
		return nil, fmt.Errorf("load checkpoints: %w", err)
	}
	defer rows.Close()

	var checkpoints []processor.Checkpoint
	for rows.Next() {
		var cp processor.Checkpoint
		var cpType int
		var since, offset, sleep sql.NullInt64
		if err := rows.Scan(&cp.ID, &cp.Sort, &cpType, &since, &offset, &sleep); err != nil {
			return nil, err
		}
		cp.Type = domain.CheckpointType(cpType)
		cp.SinceMs = nullableInt64(since)
		cp.SinceOffsetSeconds = nullableInt64(offset)
		cp.SleepAfterPrevSeconds = nullableInt64(sleep)
		checkpoints = append(checkpoints, cp)
	}
	return checkpoints, rows.Err()
}

func (r *ProcessorRepo) WithTx(ctx context.Context, fn func(tx processor.TxRepository) (bool, error)) error {
	if r.store.tx != nil {
		// The processor returns commit=false only when INSERT OR IGNORE inserted
		// nothing, before member times are touched. Any error escapes and rolls
		// back the enclosing recount transaction.
		_, err := fn(r)
		return err
	}

	tx, err := r.store.root.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	commit, err := fn(&ProcessorRepo{store: r.store, db: tx})
	if err != nil {
		tx.Rollback() //nolint:errcheck
		return err
	}
	if !commit {
		return tx.Rollback()
	}
	return tx.Commit()
}

func (r *ProcessorRepo) InsertResult(ctx context.Context, logEntry domain.RfidLog, member processor.Member, checkpoint processor.Checkpoint) (bool, error) {
	// INSERT OR IGNORE relies on uq_results_rfid_log — mirrors rfid-sync's
	// duplicate-key handling.
	res, err := r.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO results (event_id, race_id, member_id, checkpoint_id, rfid_log_id, time_ms, number)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		logEntry.EventID, member.RaceID, member.ID, checkpoint.ID, logEntry.ID, logEntry.TimeMs, member.Number)
	if err != nil {
		return false, fmt.Errorf("insert result: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// UpdateMemberTimes ports rfid-sync's semantics:
//   - backfill start from race start when the member has none,
//   - START checkpoint overwrites start_time unconditionally,
//   - FINISH sets finish_time + clean_time only once.
func (r *ProcessorRepo) UpdateMemberTimes(ctx context.Context, member processor.Member, checkpoint processor.Checkpoint, eventTimeMs int64) error {
	plan := planMemberTimes(member, checkpoint, eventTimeMs)

	switch plan.StartWrite {
	case timing.StartWriteIfNull:
		if _, err := r.db.ExecContext(ctx,
			`UPDATE members SET start_time_ms = ? WHERE id = ? AND start_time_ms IS NULL`,
			*plan.StartTimeMs, member.ID); err != nil {
			return fmt.Errorf("backfill start time: %w", err)
		}
	case timing.StartWriteReplace:
		if _, err := r.db.ExecContext(ctx,
			`UPDATE members SET start_time_ms = ? WHERE id = ?`, *plan.StartTimeMs, member.ID); err != nil {
			return fmt.Errorf("set start time: %w", err)
		}
	}

	if plan.FinishTimeMs != nil {
		var cleanTime sql.NullString
		if plan.CleanTime != nil {
			cleanTime = sql.NullString{String: *plan.CleanTime, Valid: true}
		}
		if _, err := r.db.ExecContext(ctx,
			`UPDATE members SET finish_time_ms = ?, clean_time = ? WHERE id = ? AND finish_time_ms IS NULL`,
			*plan.FinishTimeMs, cleanTime, member.ID); err != nil {
			return fmt.Errorf("set finish time: %w", err)
		}
	}

	return nil
}

func planMemberTimes(member processor.Member, checkpoint processor.Checkpoint, eventTimeMs int64) timing.MemberTimePlan {
	return timing.PlanMemberTimes(
		timing.Member[string]{
			ID: member.ID, RaceID: member.RaceID,
			StartTimeMs: member.StartTimeMs, FinishTimeMs: member.FinishTimeMs,
			RaceStartedAtMs: member.RaceStartedAtMs,
		},
		int(checkpoint.Type),
		eventTimeMs,
	)
}

func nullableInt64(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}
