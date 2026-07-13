package service

import (
	"context"
	"encoding/json"
	"fmt"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

// Offline checkpoint management. Locally created checkpoints get a "local-"
// id (survive re-imports, recognizable for sync); deletions are journaled as
// "_deleted" and re-applied after re-imports so a site export does not
// resurrect a checkpoint the judge removed on site.

type CreateCheckpointRequest struct {
	RaceID                string `json:"race_id"`
	Name                  string `json:"name"`
	Type                  int    `json:"type"` // 1=START, 2=CHECKPOINT, 3=FINISH
	Sort                  int64  `json:"sort"`
	Board                 string `json:"board"`
	SinceMs               *int64 `json:"since_ms"`
	SinceOffsetSeconds    *int64 `json:"since_offset_seconds"`
	SleepAfterPrevSeconds *int64 `json:"sleep_after_prev_seconds"`
}

func CreateCheckpoint(ctx context.Context, store *sqlite.Store, eventID string, req CreateCheckpointRequest) (string, EditResult, error) {
	if req.Board == "" {
		return "", EditResult{}, fmt.Errorf("укажите считыватель (board), например Feibot:U659")
	}
	if req.Type < 1 || req.Type > 3 {
		return "", EditResult{}, fmt.Errorf("тип чекпоинта: 1=старт, 2=КП, 3=финиш")
	}
	id := "local-" + randomHex(8)
	checkpoint := domain.Checkpoint{
		ID: id, EventID: eventID, RaceID: req.RaceID, Name: req.Name,
		Type: domain.CheckpointType(req.Type), Sort: req.Sort, Board: req.Board,
		SinceMs: req.SinceMs, SinceOffsetSeconds: req.SinceOffsetSeconds,
		SleepAfterPrevSeconds: req.SleepAfterPrevSeconds,
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return "", EditResult{}, fmt.Errorf("encode checkpoint: %w", err)
	}
	err = store.WithinTx(ctx, func(txStore *sqlite.Store) error {
		race, err := txStore.GetRace(ctx, req.RaceID)
		if err != nil || race.EventID != eventID {
			return fmt.Errorf("гонка %s не найдена в событии", req.RaceID)
		}
		if err := txStore.UpsertCheckpoint(ctx, checkpoint); err != nil {
			return err
		}
		return txStore.InsertLocalChange(ctx, sqlite.LocalChange{
			Entity: "checkpoint", EntityID: id, Field: "_created",
			OldValue: "null", NewValue: string(payload),
		})
	})
	if err != nil {
		return "", EditResult{}, err
	}

	return id, EditResult{RecountNeeded: true}, nil
}

func DeleteCheckpoint(ctx context.Context, store *sqlite.Store, eventID, checkpointID string) (EditResult, error) {
	err := store.WithinTx(ctx, func(txStore *sqlite.Store) error {
		cp, err := txStore.GetCheckpoint(ctx, checkpointID)
		if err != nil {
			return err
		}
		race, err := txStore.GetRace(ctx, cp.RaceID)
		if err != nil || race.EventID != eventID {
			return fmt.Errorf("чекпоинт %s не принадлежит событию", checkpointID)
		}
		payload, err := json.Marshal(cp)
		if err != nil {
			return fmt.Errorf("encode checkpoint: %w", err)
		}
		if err := txStore.DeleteCheckpointCascade(ctx, checkpointID); err != nil {
			return err
		}
		return txStore.InsertLocalChange(ctx, sqlite.LocalChange{
			Entity: "checkpoint", EntityID: checkpointID, Field: "_deleted",
			OldValue: string(payload), NewValue: "null",
		})
	})
	if err != nil {
		return EditResult{}, err
	}

	return EditResult{RecountNeeded: true}, nil
}
