package service

import (
	"context"
	"encoding/json"
	"fmt"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/processor"
)

// Manual finish entry — the judge's tool when a chip fails to fire: the
// finisher's time goes straight into the protocol (run5's SubmitPlace
// counterpart). Manual entries are authoritative: they survive recount wipes
// (rfid_log_id IS NULL) and are re-applied ON TOP of replayed chip data, so
// a judge-entered time always wins. To revert, delete the entry.

// ManualFinishResult reports the stored entry to the UI so it can offer undo.
type ManualFinishResult struct {
	ResultID      int64 `json:"result_id"`
	RecountNeeded bool  `json:"recount_needed"`
}

// ManualFinish stores a crossing given as wall-clock time of day.
func ManualFinish(ctx context.Context, store *sqlite.Store, eventID, memberID string, timeMs int64) (ManualFinishResult, error) {
	if timeMs <= 0 {
		return ManualFinishResult{}, fmt.Errorf("некорректное время финиша")
	}
	return storeManualFinish(ctx, store, eventID, memberID, timeMs)
}

// ManualFinishClean stores a crossing given as elapsed clean time (run5's
// ManualTimeEntry): finish = start + cleanMs, where start is the member's
// start, falling back to the race start — the same resolution the processor
// uses for chip finishes (processor_repo.go).
func ManualFinishClean(ctx context.Context, store *sqlite.Store, eventID, memberID string, cleanMs int64) (ManualFinishResult, error) {
	if cleanMs < 0 {
		return ManualFinishResult{}, fmt.Errorf("некорректное чистое время")
	}
	m, err := store.GetMember(ctx, memberID)
	if err != nil {
		return ManualFinishResult{}, err
	}
	if m.EventID != eventID {
		return ManualFinishResult{}, fmt.Errorf("участник %s не принадлежит событию", memberID)
	}
	start, err := memberStartRef(ctx, store, m)
	if err != nil {
		return ManualFinishResult{}, err
	}
	if start == nil {
		return ManualFinishResult{}, fmt.Errorf("у гонки не задано время старта — введите время суток или задайте старт гонки")
	}
	return storeManualFinish(ctx, store, eventID, memberID, *start+cleanMs)
}

// storeManualFinish inserts the result, applies the finish and journals it.
func storeManualFinish(ctx context.Context, store *sqlite.Store, eventID, memberID string, timeMs int64) (ManualFinishResult, error) {
	m, err := store.GetMember(ctx, memberID)
	if err != nil {
		return ManualFinishResult{}, err
	}
	if m.EventID != eventID {
		return ManualFinishResult{}, fmt.Errorf("участник %s не принадлежит событию", memberID)
	}

	resultID, err := store.InsertManualResult(ctx, eventID, m.RaceID, memberID, timeMs, m.Number)
	if err != nil {
		return ManualFinishResult{}, err
	}
	if err := applyManualFinish(ctx, store, memberID, timeMs); err != nil {
		return ManualFinishResult{}, err
	}

	if err := store.InsertLocalChange(ctx, sqlite.LocalChange{
		Entity: "member", EntityID: memberID, Field: "_manual_finish",
		OldValue: "null", NewValue: fmt.Sprintf(`{"time_ms":%d}`, timeMs),
	}); err != nil {
		return ManualFinishResult{}, err
	}
	return ManualFinishResult{ResultID: resultID, RecountNeeded: false}, nil
}

// ListManualFinishes returns judge entries with participant name/number for the
// live-screen review/undo list.
func ListManualFinishes(ctx context.Context, store *sqlite.Store, eventID, raceID string) ([]sqlite.ManualResultDetail, error) {
	return store.ListManualResultsDetailed(ctx, eventID, raceID)
}

// DeleteManualResult removes a judge entry; the follow-up recount restores
// chip-derived times.
func DeleteManualResult(ctx context.Context, store *sqlite.Store, eventID string, resultID int64) (EditResult, error) {
	// Capture the natural key before deleting so the deletion can sync to run5.
	mr, err := store.GetManualResult(ctx, eventID, resultID)
	if err != nil {
		return EditResult{}, err
	}
	if err := store.DeleteManualResult(ctx, eventID, resultID); err != nil {
		return EditResult{}, err
	}
	payload, err := json.Marshal(map[string]any{
		"member_id": mr.MemberID, "race_id": mr.RaceID, "time_ms": mr.TimeMs,
	})
	if err != nil {
		return EditResult{}, fmt.Errorf("encode deleted manual: %w", err)
	}
	if err := store.InsertLocalChange(ctx, sqlite.LocalChange{
		Entity: "member", EntityID: mr.MemberID, Field: "_manual_finish_deleted",
		OldValue: "null", NewValue: string(payload),
	}); err != nil {
		return EditResult{}, err
	}
	return EditResult{RecountNeeded: true}, nil
}

// applyManualFinish overrides the member's finish/clean from a manual entry.
// Clean time uses the member's start, falling back to the race start (matching
// the processor) so a chip-less finisher without a START read still gets one.
// When the start was backfilled from the race, we also persist start_time_ms —
// the ranking treats a member with a finish but no start as "not finished yet"
// and drops it from the protocol (ranking.go: materializeFixedDistance).
func applyManualFinish(ctx context.Context, store *sqlite.Store, memberID string, timeMs int64) error {
	m, err := store.GetMember(ctx, memberID)
	if err != nil {
		return err
	}
	start, err := memberStartRef(ctx, store, m)
	if err != nil {
		return err
	}
	var clean *string
	if start != nil {
		c := processor.FormatCleanTime(*start, timeMs)
		clean = &c
	}
	if start != nil && m.StartTimeMs == nil {
		// Backfill the start (race start) so the finisher ranks, mirroring the
		// processor's UpdateMemberTimes.
		_, err = store.DB().ExecContext(ctx,
			`UPDATE members SET start_time_ms = ?, finish_time_ms = ?, clean_time = ? WHERE id = ?`,
			*start, timeMs, clean, memberID)
	} else {
		_, err = store.DB().ExecContext(ctx,
			`UPDATE members SET finish_time_ms = ?, clean_time = ? WHERE id = ?`,
			timeMs, clean, memberID)
	}
	if err != nil {
		return fmt.Errorf("apply manual finish: %w", err)
	}
	return nil
}

// memberStartRef resolves the start instant for clean-time math: the member's
// own start if set, otherwise the race start (processor_repo.go does the same).
func memberStartRef(ctx context.Context, store *sqlite.Store, m domain.Member) (*int64, error) {
	if m.StartTimeMs != nil {
		return m.StartTimeMs, nil
	}
	race, err := store.GetRace(ctx, m.RaceID)
	if err != nil {
		return nil, err
	}
	return race.StartedAtMs, nil
}
