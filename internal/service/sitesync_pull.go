package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	timing "gitlab.com/fightmaster1/timing-core"
)

const changeFeedSchemaVersion = 1

type ChangePullStats struct {
	Pages        int                                   `json:"pages"`
	Observations int                                   `json:"observations"`
	Inserted     int                                   `json:"inserted"`
	Duplicates   int                                   `json:"duplicates"`
	StateChanges int                                   `json:"state_changes"`
	Recovery     bool                                  `json:"recovery"`
	Plan         timing.ProjectionPlan[string, string] `json:"-"`
	Evidence     sqlite.ProjectionFenceEvidence        `json:"-"`
	mutations    []sqlite.ObservationFeedMutation
}

// PullEventChanges consumes every currently available feed page. Each page and
// its opaque cursor are committed atomically by Store; a later page failure is
// resumed from the last complete page instead of replaying the full event.
func PullEventChanges(ctx context.Context, store *sqlite.Store, baseURL, token, eventID string, pulledAt time.Time) (ChangePullStats, error) {
	recovery, err := store.ProjectionPending(ctx, eventID)
	if err != nil {
		return ChangePullStats{}, err
	}
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
	stats := ChangePullStats{Recovery: recovery}
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
		if len(logs) == 0 && page.NextCursor == after {
			stats.Pages++
			return finalizePullProjectionPlan(ctx, store, eventID, stats)
		}
		mutations, err := store.ApplyObservationFeedPageWithMutations(ctx, eventID, logs, page.NextCursor, pulledAt.UnixMilli())
		if err != nil {
			return stats, err
		}
		for _, mutation := range mutations {
			switch mutation.Kind {
			case sqlite.ObservationFeedInserted:
				stats.Inserted++
			case sqlite.ObservationFeedStateChanged:
				stats.StateChanges++
			default:
				stats.Duplicates++
			}
		}
		stats.mutations = append(stats.mutations, mutations...)
		stats.Pages++
		stats.Observations += len(logs)
		after = page.NextCursor
		if !page.HasMore {
			return finalizePullProjectionPlan(ctx, store, eventID, stats)
		}
	}
	return stats, fmt.Errorf("change feed exceeded page safety limit")
}

func finalizePullProjectionPlan(ctx context.Context, store *sqlite.Store, eventID string, stats ChangePullStats) (ChangePullStats, error) {
	evidence, err := store.ProjectionFenceEvidence(ctx, eventID)
	if err != nil {
		return stats, err
	}
	changes := make([]timing.ProjectionChange[string, string], 0, len(stats.mutations))
	matches := make(map[string]sqlite.ObservationMemberMatch)
	for _, mutation := range stats.mutations {
		observation := mutation.Observation
		change := timing.ProjectionChange[string, string]{
			ConfigVersion: evidence.Exact.ConfigVersion, InputWatermark: evidence.Exact.InputWatermark,
			FromTimeMs: observation.TimeMs,
		}
		if mutation.Kind == sqlite.ObservationFeedDuplicate {
			change.Scope = timing.ClassifyImpact(timing.ImpactInput{
				Kind: timing.ChangeObservationAdded, Duplicate: true,
			})
			changes = append(changes, change)
			continue
		}

		identityKey := "epc:" + observation.EPC
		if observation.Number > 0 {
			identityKey = fmt.Sprintf("number:%d", observation.Number)
		}
		match, resolved := matches[identityKey]
		if !resolved {
			match, err = store.ResolveObservationMember(ctx, eventID, observation.Number, observation.EPC)
			if err != nil {
				return stats, err
			}
			matches[identityKey] = match
		}
		if match.Ambiguous {
			change.Scope = timing.ImpactReplayEvent
			changes = append(changes, change)
			continue
		}
		if !match.Found {
			kind := timing.ChangeObservationAdded
			if mutation.Kind == sqlite.ObservationFeedStateChanged {
				kind = timing.ChangeObservationState
			}
			change.Scope = timing.ClassifyImpact(timing.ImpactInput{Kind: kind})
			changes = append(changes, change)
			continue
		}

		change.MemberID = &match.MemberID
		change.RaceID = &match.RaceID
		if mutation.Kind == sqlite.ObservationFeedStateChanged {
			change.Scope = timing.ClassifyImpact(timing.ImpactInput{
				Kind: timing.ChangeObservationState, MemberKnown: true,
			})
		} else {
			// The pull adapter has not proved a direct once-pass against the
			// current progression head. Escalate safely to one member replay;
			// Stage 6 repeat semantics can later provide richer evidence.
			change.Scope = timing.ImpactReplayMember
		}
		changes = append(changes, change)
	}
	if stats.Recovery {
		changes = append(changes, timing.ProjectionChange[string, string]{
			Scope: timing.ImpactReplayEvent, ConfigVersion: evidence.Exact.ConfigVersion, InputWatermark: evidence.Exact.InputWatermark,
		})
	}
	stats.Plan = timing.BuildProjectionPlan(changes)
	stats.Evidence = evidence
	stats.mutations = nil
	return stats, nil
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
