package service

import (
	"context"
	"fmt"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/processor"
)

// Manual finish entry — the judge's tool when a chip fails to fire: the
// finisher's time goes straight into the protocol (run5's SubmitPlace
// counterpart). Manual entries are authoritative: they survive recount wipes
// (rfid_log_id IS NULL) and are re-applied ON TOP of replayed chip data, so
// a judge-entered time always wins. To revert, delete the entry.

// ManualFinish stores the crossing and applies the member's finish time.
func ManualFinish(ctx context.Context, store *sqlite.Store, eventID, memberID string, timeMs int64) (EditResult, error) {
	if timeMs <= 0 {
		return EditResult{}, fmt.Errorf("некорректное время финиша")
	}
	m, err := store.GetMember(ctx, memberID)
	if err != nil {
		return EditResult{}, err
	}
	if m.EventID != eventID {
		return EditResult{}, fmt.Errorf("участник %s не принадлежит событию", memberID)
	}

	if _, err := store.InsertManualResult(ctx, eventID, m.RaceID, memberID, timeMs, m.Number); err != nil {
		return EditResult{}, err
	}
	if err := applyManualFinish(ctx, store, memberID, timeMs); err != nil {
		return EditResult{}, err
	}

	if err := store.InsertLocalChange(ctx, sqlite.LocalChange{
		Entity: "member", EntityID: memberID, Field: "_manual_finish",
		OldValue: "null", NewValue: fmt.Sprintf(`{"time_ms":%d}`, timeMs),
	}); err != nil {
		return EditResult{}, err
	}
	return EditResult{RecountNeeded: false}, nil
}

// DeleteManualResult removes a judge entry; the follow-up recount restores
// chip-derived times.
func DeleteManualResult(ctx context.Context, store *sqlite.Store, eventID string, resultID int64) (EditResult, error) {
	if err := store.DeleteManualResult(ctx, eventID, resultID); err != nil {
		return EditResult{}, err
	}
	if err := store.InsertLocalChange(ctx, sqlite.LocalChange{
		Entity: "member", EntityID: fmt.Sprintf("result:%d", resultID), Field: "_manual_finish_deleted",
		OldValue: "null", NewValue: "null",
	}); err != nil {
		return EditResult{}, err
	}
	return EditResult{RecountNeeded: true}, nil
}

// applyManualFinish overrides the member's finish/clean from a manual entry.
func applyManualFinish(ctx context.Context, store *sqlite.Store, memberID string, timeMs int64) error {
	m, err := store.GetMember(ctx, memberID)
	if err != nil {
		return err
	}
	var clean *string
	if m.StartTimeMs != nil {
		c := processor.FormatCleanTime(*m.StartTimeMs, timeMs)
		clean = &c
	}
	_, err = store.DB().ExecContext(ctx,
		`UPDATE members SET finish_time_ms = ?, clean_time = ? WHERE id = ?`,
		timeMs, clean, memberID)
	if err != nil {
		return fmt.Errorf("apply manual finish: %w", err)
	}
	return nil
}
