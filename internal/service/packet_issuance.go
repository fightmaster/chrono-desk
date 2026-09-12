package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/packetissuance"
)

var packetCode = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

type PacketConnectionContext struct {
	ConnectionID     string
	EventID          string
	ScopeID          string
	OriginInstanceID string
}

// PacketIssuanceReceiver owns semantic admission. Authentication belongs to
// the dedicated HTTPS adapter; storage and feed publication remain atomic in
// the event Store.
type PacketIssuanceReceiver struct{}

func NewPacketIssuanceReceiver() *PacketIssuanceReceiver { return &PacketIssuanceReceiver{} }

func (r *PacketIssuanceReceiver) Receive(ctx context.Context, store *sqlite.Store, connection PacketConnectionContext, operations []packetissuance.Operation) ([]packetissuance.Receipt, error) {
	if len(operations) < 1 || len(operations) > 64 {
		return nil, errors.New("invalid_operation_batch")
	}
	receipts := make([]packetissuance.Receipt, 0, len(operations))
	for _, operation := range operations {
		receipt, err := r.receiveOne(ctx, store, connection, operation)
		if err != nil {
			return nil, err
		}
		receipts = append(receipts, receipt)
	}
	return receipts, nil
}

func (r *PacketIssuanceReceiver) receiveOne(ctx context.Context, store *sqlite.Store, connection PacketConnectionContext, operation packetissuance.Operation) (packetissuance.Receipt, error) {
	hash := packetissuance.ContentHash(operation)
	var receipt packetissuance.Receipt
	err := store.WithinTx(ctx, func(txStore *sqlite.Store) error {
		if operation.ScopeID != connection.ScopeID || operation.OriginInstanceID != connection.OriginInstanceID {
			return errors.New("operation_scope_conflict")
		}
		scope, err := txStore.GetPacketIssuanceScope(ctx, connection.EventID)
		if err != nil {
			return err
		}
		if scope.ScopeID == "" || scope.ScopeID != connection.ScopeID {
			return errors.New("operation_scope_conflict")
		}
		existing, known, err := txStore.FindPacketOperation(ctx, operation.OperationID)
		if err != nil {
			return err
		}
		if known {
			if existing.ContentHash != hash || existing.EventID != connection.EventID || existing.ScopeID != operation.ScopeID || existing.OriginInstanceID != operation.OriginInstanceID {
				return errors.New("operation_identity_conflict")
			}
			if existing.Outcome != "waiting_dependency" {
				receipt = packetReceipt(operation, hash, existing.Outcome, existing.OutcomeCode, true)
				return nil
			}
		}
		owner, found, err := txStore.FindPacketOriginSequence(ctx, operation.OriginInstanceID, operation.OriginSequence)
		if err != nil {
			return err
		}
		if found && owner.OperationID != operation.OperationID {
			return errors.New("operation_sequence_conflict")
		}
		eventID := operation.Changes[0].Before.EventID
		if eventID != connection.EventID {
			return r.persist(ctx, txStore, operation, hash, "rejected", "event_mismatch", nil, known, &receipt)
		}
		dependencies := make([]string, 0)
		seen := map[string]bool{}
		for _, base := range operation.Bases {
			for _, head := range base.Heads {
				if head == operation.OperationID {
					return r.persist(ctx, txStore, operation, hash, "rejected", "dependency_cycle", nil, known, &receipt)
				}
				if !seen[head] {
					seen[head] = true
					dependencies = append(dependencies, head)
				}
			}
		}
		applied, err := txStore.PacketDependenciesApplied(ctx, eventID, dependencies)
		if err != nil {
			return err
		}
		if !applied {
			return r.persist(ctx, txStore, operation, hash, "waiting_dependency", "dependency_missing", nil, known, &receipt)
		}
		ids := make([]string, 0, len(operation.Changes))
		for _, change := range operation.Changes {
			ids = append(ids, change.RegistrationID)
		}
		current, err := txStore.GetPacketRegistrations(ctx, eventID, ids)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return r.persist(ctx, txStore, operation, hash, "rejected", "registration_not_found", nil, known, &receipt)
			}
			return err
		}
		decision, code, changes := packetMergeDecision(current, operation, txStore, ctx)
		if decision == "applied" {
			for _, change := range changes {
				existingMember, err := txStore.GetMember(ctx, change.RegistrationID)
				if err != nil {
					return err
				}
				categoryID := existingMember.CategoryID
				if !reflect.DeepEqual(change.Before.Person, change.After.Person) {
					member := packetMember(change.After)
					categoryID, err = resolveCategoryIDForMember(ctx, txStore, member)
					if err != nil {
						return fmt.Errorf("resolve packet category %s: %w", change.RegistrationID, err)
					}
				}
				if err := txStore.PutPacketRegistration(ctx, change.After, []string{operation.OperationID}, categoryID); err != nil {
					return err
				}
			}
		}
		return r.persist(ctx, txStore, operation, hash, decision, code, changes, known, &receipt)
	})
	return receipt, err
}

func (r *PacketIssuanceReceiver) persist(ctx context.Context, store *sqlite.Store, operation packetissuance.Operation, hash, outcome, code string, changes []packetissuance.Change, known bool, receipt *packetissuance.Receipt) error {
	var codePointer *string
	if code != "" {
		if !packetCode.MatchString(code) {
			code = "transition_review_required"
		}
		codePointer = &code
	}
	when := time.Now().UnixMilli()
	if err := store.SavePacketOperation(ctx, operation, hash, outcome, codePointer, when, known); err != nil {
		return err
	}
	if err := store.PublishPacketOperation(ctx, operation, outcome, codePointer, changes, when); err != nil {
		return err
	}
	*receipt = packetReceipt(operation, hash, outcome, codePointer, known)
	return nil
}

func packetReceipt(operation packetissuance.Operation, hash, outcome string, code *string, known bool) packetissuance.Receipt {
	return packetissuance.Receipt{OperationID: operation.OperationID, ContentHash: hash, Outcome: outcome, Known: known, Code: code}
}

func packetMember(row packetissuance.Registration) domain.Member {
	member := domain.Member{ID: row.ID, EventID: row.EventID, RaceID: row.RaceID, Status: domain.StatusOK}
	if row.Person != nil {
		gender := row.Person.Gender
		dob := row.Person.BirthDate
		member.FirstName = row.Person.FirstName
		member.LastName = row.Person.LastName
		if gender != "" {
			member.Gender = &gender
		}
		if dob != "" {
			member.DOB = &dob
		}
	}
	switch row.Status {
	case "dns":
		member.Status = domain.StatusDNS
	case "dnf":
		member.Status = domain.StatusDNF
	case "dsq":
		member.Status = domain.StatusDSQ
	}
	return member
}

func packetMergeDecision(current []packetissuance.Registration, operation packetissuance.Operation, store *sqlite.Store, ctx context.Context) (string, string, []packetissuance.Change) {
	expected := operation.Changes
	for index, change := range expected {
		if !packetCompatible(change.Before, change.After, current[index]) {
			if current[index].HasTimingEvidence && !change.Before.HasTimingEvidence {
				return "conflict", "timing_review_required", nil
			}
			return "conflict", "current_state_changed", nil
		}
	}
	equivalent := true
	for index, change := range expected {
		equivalent = equivalent && packetChangedEqual(change.Before, change.After, current[index], change.After)
	}
	if equivalent {
		return "equivalent", "", nil
	}
	var derived []packetissuance.Change
	var err error
	if operation.Command.Type == "correct_move" {
		record, found, findErr := store.FindPacketOperation(ctx, operation.Command.OperationID)
		if findErr != nil || !found || (record.Outcome != "applied" && record.Outcome != "equivalent") {
			return "waiting_dependency", "correction_dependency_missing", nil
		}
		original, parseErr := packetissuance.ParseOperation(record.OperationJSON)
		if parseErr != nil {
			return "conflict", "correction_review_required", nil
		}
		derived, err = packetissuance.ReverseMove(current, original.Changes, operation.Command.Reason)
	} else {
		derived, err = packetissuance.Apply(current, operation.Command)
	}
	if err != nil {
		code := err.Error()
		if !packetCode.MatchString(code) {
			code = "transition_review_required"
		}
		return "conflict", code, nil
	}
	byID := map[string]packetissuance.Change{}
	for _, change := range derived {
		byID[change.RegistrationID] = change
	}
	for _, change := range expected {
		actual, ok := byID[change.RegistrationID]
		if !ok {
			actual = packetissuance.Change{RegistrationID: change.RegistrationID, Before: currentRegistration(current, change.RegistrationID), After: currentRegistration(current, change.RegistrationID)}
		}
		if !packetEffectMatches(change.Before, change.After, actual.Before, actual.After) {
			return "conflict", "operation_effect_changed", nil
		}
	}
	return "applied", "", derived
}

func currentRegistration(rows []packetissuance.Registration, id string) packetissuance.Registration {
	for _, row := range rows {
		if row.ID == id {
			return row
		}
	}
	return packetissuance.Registration{}
}
func packetValue(value any) any {
	data, _ := json.Marshal(value)
	var decoded any
	_ = json.Unmarshal(data, &decoded)
	return decoded
}
func packetCompatible(before, after, current any) bool {
	return recursiveCompatible(packetValue(before), packetValue(after), packetValue(current))
}
func recursiveCompatible(before, after, current any) bool {
	if reflect.DeepEqual(before, after) {
		return true
	}
	bm, bok := before.(map[string]any)
	am, aok := after.(map[string]any)
	cm, cok := current.(map[string]any)
	if bok && aok && cok && sameKeys(bm, am) && sameKeys(bm, cm) {
		for key, value := range bm {
			if !recursiveCompatible(value, am[key], cm[key]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(current, before) || reflect.DeepEqual(current, after)
}
func packetChangedEqual(before, after, left, right any) bool {
	return recursiveChangedEqual(packetValue(before), packetValue(after), packetValue(left), packetValue(right))
}
func recursiveChangedEqual(before, after, left, right any) bool {
	if reflect.DeepEqual(before, after) {
		return true
	}
	bm, bok := before.(map[string]any)
	am, aok := after.(map[string]any)
	lm, lok := left.(map[string]any)
	rm, rok := right.(map[string]any)
	if bok && aok && lok && rok && sameKeys(bm, am) && sameKeys(bm, lm) && sameKeys(bm, rm) {
		for key, value := range bm {
			if !recursiveChangedEqual(value, am[key], lm[key], rm[key]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(left, right)
}
func packetEffectMatches(expectedBefore, expectedAfter, actualBefore, actualAfter any) bool {
	return recursiveEffect(packetValue(expectedBefore), packetValue(expectedAfter), packetValue(actualBefore), packetValue(actualAfter))
}
func recursiveEffect(before, after, actualBefore, actualAfter any) bool {
	if reflect.DeepEqual(before, after) {
		return reflect.DeepEqual(actualBefore, actualAfter)
	}
	bm, bok := before.(map[string]any)
	am, aok := after.(map[string]any)
	abm, abok := actualBefore.(map[string]any)
	aam, aaok := actualAfter.(map[string]any)
	if bok && aok && abok && aaok && sameKeys(bm, am) && sameKeys(bm, abm) && sameKeys(bm, aam) {
		for key, value := range bm {
			if !recursiveEffect(value, am[key], abm[key], aam[key]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(actualAfter, after) && (reflect.DeepEqual(actualBefore, before) || reflect.DeepEqual(actualBefore, after))
}
func sameKeys(left, right map[string]any) bool {
	if len(left) != len(right) {
		return false
	}
	keys := make([]string, 0, len(left))
	for key := range left {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, ok := right[key]; !ok {
			return false
		}
	}
	return true
}
