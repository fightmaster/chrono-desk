package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/packetissuance"
)

type PacketSnapshotApplication struct {
	RegistrationID string  `json:"registration_id"`
	Application    string  `json:"application"`
	Code           *string `json:"code,omitempty"`
}

type PacketSnapshotRebaseResult struct {
	Applied int                         `json:"applied"`
	Review  int                         `json:"review"`
	Items   []PacketSnapshotApplication `json:"items"`
}

// RebasePacketIssuanceRoster advances an authenticated site snapshot without
// replaying it over the current Desk projection. The separately persisted site
// value is the merge base; pending local values and causal heads are retained.
func RebasePacketIssuanceRoster(ctx context.Context, store *sqlite.Store, eventID string,
	snapshot packetissuance.Bootstrap) (PacketSnapshotRebaseResult, error) {
	var result PacketSnapshotRebaseResult
	err := store.WithinTx(ctx, func(txStore *sqlite.Store) error {
		scope, err := txStore.GetPacketIssuanceScope(ctx, eventID)
		if err != nil {
			return err
		}
		if snapshot.SchemaVersion != 2 || snapshot.SourceKind != "site" || snapshot.Event.ID != eventID ||
			snapshot.ScopeID == "" || snapshot.ScopeID != scope.ScopeID || snapshot.BaselineID == "" ||
			snapshot.FeedCursor == "" || !validPacketSnapshotCursor(snapshot.FeedCursor) || len(snapshot.Registrations) > 20_000 {
			return errors.New("invalid_packet_snapshot_rebase")
		}
		complete, err := txStore.PacketSiteBaselineComplete(ctx, eventID)
		if err != nil {
			return err
		}
		if !complete {
			return errors.New("packet_site_baseline_unavailable")
		}

		incoming := make(map[string]packetissuance.Registration, len(snapshot.Registrations))
		for _, row := range snapshot.Registrations {
			if err := packetissuance.ValidateRegistration(row); err != nil || row.EventID != eventID {
				return errors.New("invalid_packet_snapshot_rebase")
			}
			if _, exists := incoming[row.ID]; exists {
				return errors.New("invalid_packet_snapshot_rebase")
			}
			incoming[row.ID] = row
		}

		baselines, err := txStore.ListPacketSiteRegistrations(ctx, eventID)
		if err != nil {
			return err
		}
		baselineByID := make(map[string]sqlite.PacketSiteRegistrationRecord, len(baselines))
		for _, baseline := range baselines {
			baselineByID[baseline.Value.ID] = baseline
		}

		plans := make([]packetFeedPlan, 0, len(incoming))
		baselineChanges := make([]packetissuance.Registration, 0, len(incoming))
		for _, serverValue := range snapshot.Registrations {
			baseline, hasBaseline := baselineByID[serverValue.ID]
			if hasBaseline && !baseline.Deleted && reflect.DeepEqual(packetFeedProjection(baseline.Value), packetFeedProjection(serverValue)) {
				continue
			}
			current, err := txStore.FindPacketRegistration(ctx, eventID, serverValue.ID)
			if err != nil {
				return err
			}
			var before *packetissuance.Registration
			if hasBaseline && !baseline.Deleted {
				value := baseline.Value
				before = &value
			}
			after := serverValue
			change := packetissuance.FeedChange{RegistrationID: serverValue.ID, Before: before, After: &after}
			merged, code := mergePacketFeedChange(current, change)
			if code == "" {
				if merged != nil {
					merged.HasTimingEvidence = merged.HasTimingEvidence || serverValue.HasTimingEvidence
				}
				plans = append(plans, packetFeedPlan{change: change, before: current, after: merged, heads: append([]string(nil), current.Heads...)})
				result.Items = append(result.Items, PacketSnapshotApplication{RegistrationID: serverValue.ID, Application: "applied"})
				result.Applied++
			} else {
				codeCopy := code
				result.Items = append(result.Items, PacketSnapshotApplication{RegistrationID: serverValue.ID, Application: "review", Code: &codeCopy})
				result.Review++
				if current.Found && !current.Deleted && serverValue.HasTimingEvidence && !current.Value.HasTimingEvidence {
					retained := current.Value
					retained.HasTimingEvidence = true
					plans = append(plans, packetFeedPlan{change: change, before: current, after: &retained, heads: append([]string(nil), current.Heads...)})
				}
			}
			baselineChanges = append(baselineChanges, serverValue)
		}

		for _, baseline := range baselines {
			if baseline.Deleted {
				continue
			}
			if _, exists := incoming[baseline.Value.ID]; exists {
				continue
			}
			code := "snapshot_registration_missing"
			result.Items = append(result.Items, PacketSnapshotApplication{
				RegistrationID: baseline.Value.ID, Application: "review", Code: &code,
			})
			result.Review++
		}

		if err := applyPacketFeedPlans(ctx, txStore, eventID, plans); err != nil {
			return err
		}
		for index := range baselineChanges {
			if err := txStore.PutPacketSiteRegistration(ctx, eventID, &baselineChanges[index]); err != nil {
				return err
			}
		}
		sort.Slice(result.Items, func(i, j int) bool {
			return result.Items[i].RegistrationID < result.Items[j].RegistrationID
		})
		snapshotJSON, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		applicationsJSON, err := json.Marshal(result.Items)
		if err != nil {
			return err
		}
		if err := txStore.SavePacketSnapshotRebase(ctx, sqlite.PacketSnapshotRebaseRecord{
			EventID: eventID, ScopeID: scope.ScopeID, BeforeBaselineID: scope.BaselineID,
			AfterBaselineID: snapshot.BaselineID, BeforeCursor: scope.SiteFeedCursor,
			AfterCursor: snapshot.FeedCursor, SnapshotJSON: snapshotJSON,
			ApplicationsJSON: applicationsJSON, RecordedAt: time.Now().UnixMilli(),
		}); err != nil {
			return err
		}
		return txStore.CompletePacketSnapshotRebase(ctx, eventID, scope.BaselineID,
			snapshot.BaselineID, scope.SiteFeedCursor, snapshot.FeedCursor)
	})
	return result, err
}

func validPacketSnapshotCursor(value string) bool {
	parsed, err := strconv.ParseInt(value, 10, 64)
	return err == nil && parsed >= 0 && strconv.FormatInt(parsed, 10) == value
}
