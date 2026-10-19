package service

import (
	"context"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

// LoadMergedFinishes loads local metadata only. Network image downloads must
// happen after the caller releases its coherent database snapshot.
func LoadMergedFinishes(ctx context.Context, store *sqlite.Store, eventID string, windowMs int64, limit int) ([]MergedFinish, error) {
	photos, err := store.ListPhotosForMerge(ctx, eventID)
	if err != nil {
		return nil, err
	}
	finishes := MergeFinishes(photos, windowMs)
	if limit > 0 && len(finishes) > limit {
		finishes = finishes[:limit]
	}
	return hydrateMergedFrames(ctx, store, eventID, finishes)
}

func hydrateMergedFrames(ctx context.Context, store photoMergeStore, eventID string, finishes []MergedFinish) ([]MergedFinish, error) {
	ids := make([]string, len(finishes))
	for i := range finishes {
		ids[i] = finishes[i].ID
	}
	frames, err := store.GetFramesByIDs(ctx, eventID, ids)
	if err != nil {
		return nil, err
	}
	for i := range finishes {
		if value, ok := frames[finishes[i].ID]; ok {
			finishes[i].Frames = value
		}
	}
	return finishes, nil
}
