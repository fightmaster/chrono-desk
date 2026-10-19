package httpapi

import (
	"context"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/service"
)

// Return fully loaded data after releasing its coherent read lease. Slow JSON
// encoding or clients must not hold a WAL snapshot open.
func readEventView[T any](ctx context.Context, events *service.EventService, eventID string, load func(*sqlite.Store) (T, error)) (T, error) {
	var result T
	err := events.WithReadSnapshot(ctx, eventID, func(read *sqlite.Store) error {
		var err error
		result, err = load(read)
		return err
	})
	return result, err
}
