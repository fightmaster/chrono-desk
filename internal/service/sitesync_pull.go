package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

const changeFeedSchemaVersion = 1

type ChangePullStats struct {
	Pages        int `json:"pages"`
	Observations int `json:"observations"`
}

// PullEventChanges consumes every currently available feed page. Each page and
// its opaque cursor are committed atomically by Store; a later page failure is
// resumed from the last complete page instead of replaying the full event.
func PullEventChanges(ctx context.Context, store *sqlite.Store, baseURL, token, eventID string, pulledAt time.Time) (ChangePullStats, error) {
	capabilities, err := GetSyncCapabilities(ctx, baseURL, token, eventID)
	if err != nil {
		return ChangePullStats{}, err
	}
	if !SupportsChangeFeed(capabilities, changeFeedSchemaVersion) {
		return ChangePullStats{}, fmt.Errorf("сайт не поддерживает безопасный change feed v%d", changeFeedSchemaVersion)
	}
	cursor, err := store.GetPullCursor(ctx, eventID)
	if err != nil {
		return ChangePullStats{}, err
	}
	after := ""
	if cursor != nil {
		after = *cursor
	}
	var stats ChangePullStats
	for pageNumber := 0; pageNumber < 10_000; pageNumber++ {
		page, err := PullChangeFeedPage(ctx, baseURL, token, eventID, after, 500)
		if err != nil {
			return stats, err
		}
		logs := make([]domain.RfidLog, 0, len(page.Items))
		for _, item := range page.Items {
			if item.Type != "observation_created" && item.Type != "observation_state_changed" {
				return stats, fmt.Errorf("change feed item type %q is not supported", item.Type)
			}
			logEntry, err := feedObservation(item.Observation)
			if err != nil {
				return stats, err
			}
			logs = append(logs, logEntry)
		}
		if page.HasMore && (len(logs) == 0 || page.NextCursor == after) {
			return stats, fmt.Errorf("change feed page does not advance cursor")
		}
		if err := store.ApplyObservationFeedPage(ctx, eventID, logs, page.NextCursor, pulledAt.UnixMilli()); err != nil {
			return stats, err
		}
		stats.Pages++
		stats.Observations += len(logs)
		after = page.NextCursor
		if !page.HasMore {
			return stats, nil
		}
	}
	return stats, fmt.Errorf("change feed exceeded page safety limit")
}

func feedObservation(input ChangeFeedObservation) (domain.RfidLog, error) {
	if strings.TrimSpace(input.ID) == "" || strings.TrimSpace(input.EventID) == "" || strings.TrimSpace(input.Board) == "" {
		return domain.RfidLog{}, fmt.Errorf("change feed observation identity is incomplete")
	}
	var disabledAt *int64
	if input.DisabledAt != nil {
		parsed, err := time.Parse(time.RFC3339Nano, *input.DisabledAt)
		if err != nil {
			return domain.RfidLog{}, fmt.Errorf("observation %s disabled_at: %w", input.ID, err)
		}
		value := parsed.UnixMilli()
		disabledAt = &value
	}
	return domain.RfidLog{
		ID: input.ID, EventID: input.EventID, Status: input.Status, Number: input.Number,
		TimeMs: input.TimeMs, Ant: input.Ant, EPC: input.EPC, RSSI: input.RSSI, Board: input.Board,
		DisabledAt: disabledAt, ObservationVersion: input.ObservationVersion,
		CaptureSourceID: input.CaptureSourceID, OriginSystem: input.OriginSystem,
		OriginInstanceID: input.OriginInstanceID, OriginSequence: input.OriginSequence,
	}, nil
}
