package service

import (
	"context"
	"errors"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	projection "gitlab.com/fightmaster1/timing-core/eventprojection"
)

// This optional adapter keeps unsupported formats/scopes on the established Go
// replay. The shared engine, not this adapter, owns calculation rules.
type snapshotRecountStore interface {
	LoadFixedDistanceSnapshot(context.Context, string) (projection.Snapshot[string], error)
	StreamSnapshotReadings(context.Context, string, string, func(projection.Reading[string]) error) (int, error)
	ReplaceFixedDistanceProjection(context.Context, string, string, projection.Projection[string]) error
}

func (r *Recounter) trySnapshotRecount(ctx context.Context, store recountTxStore, eventID, raceID string) (RecountStats, bool, error) {
	adapter, supported := store.(snapshotRecountStore)
	if !supported {
		return RecountStats{}, false, nil
	}
	input, err := adapter.LoadFixedDistanceSnapshot(ctx, eventID)
	if errors.Is(err, sqlite.ErrSnapshotUnsupported) {
		return RecountStats{}, false, nil
	}
	if err != nil {
		return RecountStats{}, true, err
	}
	count := 0
	result, err := projection.ComputeOrderedFixedDistance(ctx, input, func(yield func(projection.Reading[string]) error) error {
		var readErr error
		count, readErr = adapter.StreamSnapshotReadings(ctx, eventID, raceID, yield)
		return readErr
	})
	if err != nil {
		return RecountStats{}, true, err
	}
	if err := adapter.ReplaceFixedDistanceProjection(ctx, eventID, raceID, result); err != nil {
		return RecountStats{}, true, err
	}
	if r.logger != nil {
		r.logger.Printf("recount calculator=%s event=%s logs=%d", projection.Version, eventID, count)
	}
	return RecountStats{LogsReplayed: count}, true, nil
}

func (s sqliteRecountTxStore) LoadFixedDistanceSnapshot(ctx context.Context, eventID string) (projection.Snapshot[string], error) {
	return s.store.LoadFixedDistanceSnapshot(ctx, eventID)
}

func (s sqliteRecountTxStore) StreamSnapshotReadings(ctx context.Context, eventID, raceID string, yield func(projection.Reading[string]) error) (int, error) {
	return s.store.StreamSnapshotReadings(ctx, eventID, raceID, yield)
}

func (s sqliteRecountTxStore) ReplaceFixedDistanceProjection(ctx context.Context, eventID, raceID string, result projection.Projection[string]) error {
	return s.store.ReplaceFixedDistanceProjection(ctx, eventID, raceID, result)
}
