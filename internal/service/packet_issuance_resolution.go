package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/packetissuance"
)

type PacketConflictPage struct {
	Items  []PacketConflictCandidate `json:"items"`
	Total  int                       `json:"total"`
	Limit  int                       `json:"limit"`
	Offset int                       `json:"offset"`
}

type PacketConflictCandidate struct {
	OperationID        string                  `json:"operation_id"`
	ClaimedActor       string                  `json:"claimed_actor"`
	Code               *string                 `json:"code"`
	RecordedAt         int64                   `json:"recorded_at"`
	Resolved           bool                    `json:"resolved"`
	ProposalApplicable bool                    `json:"proposal_applicable"`
	Evidence           string                  `json:"evidence"`
	Changes            []packetissuance.Change `json:"changes"`
}

func ListPacketConflictCandidates(ctx context.Context, store *sqlite.Store, eventID string, unresolvedOnly bool, limit, offset int) (PacketConflictPage, error) {
	records, total, err := store.ListPacketConflicts(ctx, eventID, unresolvedOnly, limit, offset)
	if err != nil {
		return PacketConflictPage{}, err
	}
	page := PacketConflictPage{Items: make([]PacketConflictCandidate, 0, len(records)), Total: total, Limit: limit, Offset: offset}
	for _, record := range records {
		candidate, err := packetConflictCandidate(ctx, store, eventID, record)
		if err != nil {
			return PacketConflictPage{}, err
		}
		page.Items = append(page.Items, candidate)
	}
	return page, nil
}

func ResolvePacketConflict(ctx context.Context, events *EventService, eventID, operationID string, applyProposal bool, reason, actor, expectedEvidence string) (packetissuance.Operation, error) {
	reason, actor = strings.TrimSpace(reason), strings.TrimSpace(actor)
	if reason == "" || len([]rune(reason)) > 500 || actor == "" || len([]rune(actor)) > 160 {
		return packetissuance.Operation{}, errors.New("invalid packet conflict resolution")
	}
	sequence, err := events.NextInstallationSequence()
	if err != nil {
		return packetissuance.Operation{}, err
	}
	store, err := events.Open(eventID)
	if err != nil {
		return packetissuance.Operation{}, err
	}
	var resolution packetissuance.Operation
	err = store.WithinTx(ctx, func(txStore *sqlite.Store) error {
		record, found, err := txStore.FindPacketOperation(ctx, operationID)
		if err != nil {
			return err
		}
		if !found || record.EventID != eventID || record.Outcome != "conflict" {
			return errors.New("packet_resolution_input_missing")
		}
		if _, decided, err := txStore.FindPacketResolutionByInput(ctx, operationID); err != nil {
			return err
		} else if decided {
			return errors.New("packet_resolution_already_decided")
		}
		candidate, err := packetConflictCandidate(ctx, txStore, eventID, sqlite.PacketConflictRecord{Operation: record})
		if err != nil {
			return err
		}
		if candidate.Evidence != expectedEvidence {
			return errors.New("packet_resolution_stale")
		}
		if applyProposal && !candidate.ProposalApplicable {
			return errors.New("packet_resolution_proposal_not_applicable")
		}
		resolutionID := uuid.NewString()
		changes := candidate.Changes
		if !applyProposal {
			changes = reversePacketChanges(changes)
		} else {
			for _, change := range changes {
				member, err := txStore.GetMember(ctx, change.RegistrationID)
				if err != nil {
					return err
				}
				categoryID := member.CategoryID
				if !reflect.DeepEqual(change.Before.Person, change.After.Person) {
					categoryID, err = resolveCategoryIDForMember(ctx, txStore, packetMember(change.After))
					if err != nil {
						return err
					}
				}
				if err := txStore.PutPacketRegistration(ctx, change.After, []string{resolutionID}, categoryID); err != nil {
					return err
				}
			}
		}
		now := time.Now().UTC()
		raw, err := json.Marshal(map[string]any{
			"schemaVersion": 2, "operationId": resolutionID, "scopeId": record.ScopeID,
			"baselineId": record.BaselineID, "originInstanceId": events.InstallationID(),
			"originSequence": sequence, "createdAtMs": now.UnixMilli(), "claimedActor": actor,
			"command": map[string]any{"type": "resolve_conflict", "inputs": []string{operationID},
				"keep": func() []string {
					if applyProposal {
						return []string{operationID}
					}
					return []string{}
				}(), "reason": reason},
			"bases": packetResolutionBases(changes, operationID), "changes": changes,
		})
		if err != nil {
			return err
		}
		resolution, err = packetissuance.ParseTrustedOperation(raw)
		if err != nil {
			return fmt.Errorf("build packet resolution: %w", err)
		}
		recordedAt := now.UnixMilli()
		if err := txStore.SavePacketOperation(ctx, resolution, packetissuance.ContentHash(resolution), "applied", nil, recordedAt, false); err != nil {
			return err
		}
		if err := txStore.SavePacketResolution(ctx, sqlite.PacketResolutionRecord{
			ResolutionOperationID: resolution.OperationID, EventID: eventID, InputOperationID: operationID,
			KeepInput: applyProposal, Reason: reason, RecordedAt: recordedAt,
		}); err != nil {
			return err
		}
		return txStore.PublishPacketOperation(ctx, resolution, "applied", nil, changes, recordedAt)
	})
	return resolution, err
}

func packetConflictCandidate(ctx context.Context, store *sqlite.Store, eventID string, record sqlite.PacketConflictRecord) (PacketConflictCandidate, error) {
	operation, err := packetissuance.ParseOperation(record.Operation.OperationJSON)
	if err != nil || operation.SchemaVersion != 1 {
		return PacketConflictCandidate{}, errors.New("stored packet conflict is invalid")
	}
	ids := make([]string, 0, len(operation.Changes))
	for _, change := range operation.Changes {
		ids = append(ids, change.RegistrationID)
	}
	current, err := store.GetPacketRegistrations(ctx, eventID, ids)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PacketConflictCandidate{}, errors.New("packet_resolution_input_missing")
		}
		return PacketConflictCandidate{}, err
	}
	derived, applyErr := packetissuance.Apply(current, operation.Command)
	applicable := applyErr == nil
	if !applicable {
		derived, err = convergencePacketChanges(current, operation.Changes)
		if err != nil {
			return PacketConflictCandidate{}, err
		}
	}
	changes := normalizePacketCandidate(current, derived)
	evidenceRaw, _ := json.Marshal(struct {
		Hash       string                  `json:"inputContentHash"`
		Changes    []packetissuance.Change `json:"changes"`
		Applicable bool                    `json:"proposalApplicable"`
	}{record.Operation.ContentHash, changes, applicable})
	sum := sha256.Sum256(evidenceRaw)
	return PacketConflictCandidate{
		OperationID: operation.OperationID, ClaimedActor: operation.ClaimedActor,
		Code: record.Operation.OutcomeCode, RecordedAt: record.Operation.RecordedAt, Resolved: record.Resolved,
		ProposalApplicable: applicable, Evidence: "sha256:" + hex.EncodeToString(sum[:]), Changes: changes,
	}, nil
}

func normalizePacketCandidate(current []packetissuance.Registration, derived []packetissuance.Change) []packetissuance.Change {
	byID := make(map[string]packetissuance.Registration, len(derived))
	for _, change := range derived {
		byID[change.RegistrationID] = change.After
	}
	result := make([]packetissuance.Change, 0, len(current))
	for _, row := range current {
		after, found := byID[row.ID]
		if !found {
			after = row
		}
		result = append(result, packetissuance.Change{RegistrationID: row.ID, Before: row, After: after})
	}
	return result
}

func convergencePacketChanges(current []packetissuance.Registration, expected []packetissuance.Change) ([]packetissuance.Change, error) {
	byID := make(map[string]packetissuance.Registration, len(current))
	for _, row := range current {
		byID[row.ID] = row
	}
	result := make([]packetissuance.Change, 0, len(expected))
	for _, change := range expected {
		row, found := byID[change.RegistrationID]
		if !found {
			return nil, errors.New("packet_resolution_input_missing")
		}
		after, err := overlayPacketEffect(change.Before, change.After, row)
		if err != nil {
			return nil, err
		}
		result = append(result, packetissuance.Change{RegistrationID: row.ID, Before: row, After: after})
	}
	return result, nil
}

func overlayPacketEffect(before, after, current packetissuance.Registration) (packetissuance.Registration, error) {
	b, err := packetJSONValue(before)
	if err != nil {
		return current, err
	}
	a, err := packetJSONValue(after)
	if err != nil {
		return current, err
	}
	c, err := packetJSONValue(current)
	if err != nil {
		return current, err
	}
	merged := overlayPacketValue(b, a, c)
	raw, _ := json.Marshal(merged)
	var result packetissuance.Registration
	if err := json.Unmarshal(raw, &result); err != nil {
		return current, err
	}
	return result, packetissuance.ValidateRegistration(result)
}

func packetJSONValue(value any) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func overlayPacketValue(before, after, current any) any {
	if reflect.DeepEqual(before, after) {
		return current
	}
	bm, bok := before.(map[string]any)
	am, aok := after.(map[string]any)
	cm, cok := current.(map[string]any)
	if bok && aok && cok && len(bm) == len(am) && len(bm) == len(cm) {
		result := make(map[string]any, len(cm))
		for key, value := range cm {
			result[key] = value
		}
		for key, value := range bm {
			result[key] = overlayPacketValue(value, am[key], cm[key])
		}
		return result
	}
	return after
}

func reversePacketChanges(changes []packetissuance.Change) []packetissuance.Change {
	result := make([]packetissuance.Change, len(changes))
	for index, change := range changes {
		result[index] = packetissuance.Change{RegistrationID: change.RegistrationID, Before: change.After, After: change.Before}
	}
	return result
}

func packetResolutionBases(changes []packetissuance.Change, input string) []packetissuance.Base {
	result := make([]packetissuance.Base, len(changes))
	for index, change := range changes {
		result[index] = packetissuance.Base{RegistrationID: change.RegistrationID, Heads: []string{input}}
	}
	return result
}
