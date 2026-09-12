package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/packetissuance"
)

type PacketFeedApplication struct {
	ActionID    string  `json:"action_id"`
	Application string  `json:"application"`
	Code        *string `json:"code,omitempty"`
	Known       bool    `json:"known"`
}

// ApplyPacketFeedPage commits the untrusted site page, projection changes and
// cursor as one unit. Incompatible concurrent edits are retained for review;
// they never replace the local projection.
func ApplyPacketFeedPage(ctx context.Context, store *sqlite.Store, eventID string, page packetissuance.FeedPage) ([]PacketFeedApplication, error) {
	applications := make([]PacketFeedApplication, 0, len(page.Actions))
	err := store.WithinTx(ctx, func(txStore *sqlite.Store) error {
		scope, err := txStore.GetPacketIssuanceScope(ctx, eventID)
		if err != nil {
			return err
		}
		if scope.ScopeID == "" || scope.ScopeID != page.ScopeID || scope.SiteFeedCursor != page.Cursor.After {
			return errors.New("packet_feed_cursor_changed")
		}
		for _, action := range page.Actions {
			known, found, err := txStore.FindPacketSiteFeedAction(ctx, action.ActionID)
			if err != nil {
				return err
			}
			if found {
				if known.EventID != eventID || known.SiteSequence != action.Sequence ||
					!reflect.DeepEqual(known.ActionJSON, action.CanonicalJSON()) {
					return errors.New("packet_feed_action_identity_conflict")
				}
				applications = append(applications, PacketFeedApplication{ActionID: action.ActionID,
					Application: known.Application, Code: known.ApplicationCode, Known: true})
				continue
			}
			if action.Operation != nil {
				local, exists, err := txStore.FindPacketOperation(ctx, action.ActionID)
				if err != nil {
					return err
				}
				if exists && !reflect.DeepEqual(local.OperationJSON, action.Operation.CanonicalJSON()) {
					return errors.New("packet_feed_action_identity_conflict")
				}
			}
			application, code := "observed", action.Code
			if action.Outcome == "conflict" {
				application = "review"
			} else if action.Outcome == "applied" {
				application, code, err = applyPacketFeedChanges(ctx, txStore, eventID, action)
				if err != nil {
					return err
				}
			}
			recordedAt, err := time.Parse("2006-01-02T15:04:05.000Z", action.RecordedAt)
			if err != nil {
				return errors.New("invalid_feed_response")
			}
			if err := txStore.SavePacketSiteFeedAction(ctx, sqlite.PacketSiteFeedRecord{
				ActionID: action.ActionID, EventID: eventID, SiteSequence: action.Sequence,
				ActionJSON: action.CanonicalJSON(), Application: application,
				ApplicationCode: code, RecordedAt: recordedAt.UnixMilli(),
			}); err != nil {
				return err
			}
			applications = append(applications, PacketFeedApplication{ActionID: action.ActionID,
				Application: application, Code: code})
		}
		return txStore.AdvancePacketSiteCursor(ctx, eventID, page.Cursor.After, page.Cursor.Next)
	})
	return applications, err
}

type packetFeedPlan struct {
	change packetissuance.FeedChange
	before sqlite.PacketRegistrationRecord
	after  *packetissuance.Registration
	heads  []string
}

func applyPacketFeedChanges(ctx context.Context, store *sqlite.Store, eventID string, action packetissuance.FeedAction) (string, *string, error) {
	plans := make([]packetFeedPlan, 0, len(action.Changes))
	for _, change := range action.Changes {
		current, err := store.FindPacketRegistration(ctx, eventID, change.RegistrationID)
		if err != nil {
			return "", nil, err
		}
		merged, code := mergePacketFeedChange(current, change)
		if code != "" {
			return "review", &code, nil
		}
		heads := append([]string(nil), current.Heads...)
		heads = append(heads, action.ActionID)
		heads = uniquePacketHeads(heads)
		if len(heads) > 64 {
			code := "feed_head_limit"
			return "review", &code, nil
		}
		plans = append(plans, packetFeedPlan{change: change, before: current, after: merged, heads: heads})
	}
	for _, plan := range plans {
		if plan.after == nil {
			if !plan.before.Found || plan.before.Deleted {
				continue
			}
			if err := store.DeletePacketRegistration(ctx, eventID, plan.change.RegistrationID, plan.heads); err != nil {
				return "", nil, err
			}
			continue
		}
		member := packetMember(*plan.after)
		categoryID, err := resolveCategoryIDForMember(ctx, store, member)
		if err != nil {
			return "", nil, fmt.Errorf("resolve packet category %s: %w", plan.change.RegistrationID, err)
		}
		if !plan.before.Found {
			err = store.InsertPacketRegistration(ctx, *plan.after, plan.heads, categoryID)
		} else {
			err = store.PutPacketRegistration(ctx, *plan.after, plan.heads, categoryID)
		}
		if err != nil {
			return "", nil, err
		}
	}
	return "applied", nil, nil
}

func mergePacketFeedChange(current sqlite.PacketRegistrationRecord, change packetissuance.FeedChange) (*packetissuance.Registration, string) {
	var currentValue *packetissuance.Registration
	if current.Found && !current.Deleted {
		value := current.Value
		currentValue = &value
	}
	if change.Before == nil {
		if currentValue == nil {
			value := *change.After
			value.HasTimingEvidence = false
			return &value, ""
		}
		if reflect.DeepEqual(packetFeedProjection(*currentValue), packetFeedProjection(*change.After)) {
			value := *currentValue
			return &value, ""
		}
		return nil, "feed_lifecycle_conflict"
	}
	if change.After == nil {
		if currentValue == nil {
			return nil, ""
		}
		if reflect.DeepEqual(packetFeedProjection(*currentValue), packetFeedProjection(*change.Before)) {
			return nil, ""
		}
		return nil, "feed_lifecycle_conflict"
	}
	if currentValue == nil {
		return nil, "feed_lifecycle_conflict"
	}
	merged, ok := mergePacketFeedValue(packetFeedProjection(*change.Before), packetFeedProjection(*change.After), packetFeedProjection(*currentValue))
	if !ok {
		return nil, "feed_state_conflict"
	}
	raw, err := json.Marshal(merged)
	if err != nil {
		return nil, "feed_state_conflict"
	}
	var result packetissuance.Registration
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, "feed_state_conflict"
	}
	result.HasTimingEvidence = currentValue.HasTimingEvidence
	return &result, ""
}

func packetFeedProjection(value packetissuance.Registration) map[string]any {
	raw, _ := json.Marshal(value)
	var projection map[string]any
	_ = json.Unmarshal(raw, &projection)
	delete(projection, "hasTimingEvidence")
	return projection
}

func mergePacketFeedValue(before, after, current any) (any, bool) {
	if reflect.DeepEqual(before, after) {
		return current, true
	}
	beforeMap, beforeOK := before.(map[string]any)
	afterMap, afterOK := after.(map[string]any)
	currentMap, currentOK := current.(map[string]any)
	if beforeOK && afterOK && currentOK && sameKeys(beforeMap, afterMap) && sameKeys(beforeMap, currentMap) {
		merged := make(map[string]any, len(beforeMap))
		for key, beforeValue := range beforeMap {
			value, ok := mergePacketFeedValue(beforeValue, afterMap[key], currentMap[key])
			if !ok {
				return nil, false
			}
			merged[key] = value
		}
		return merged, true
	}
	if reflect.DeepEqual(current, before) || reflect.DeepEqual(current, after) {
		return after, true
	}
	return nil, false
}

func uniquePacketHeads(heads []string) []string {
	seen := make(map[string]bool, len(heads))
	result := make([]string, 0, len(heads))
	for _, head := range heads {
		if head != "" && !seen[head] {
			seen[head] = true
			result = append(result, head)
		}
	}
	sort.Strings(result)
	return result
}
