package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"

	"github.com/google/uuid"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

var ErrClubIdentityConflict = errors.New("club_identity_conflict")
var ErrInvalidClubOperation = errors.New("invalid_club_operation")
var clubIdentifier = regexp.MustCompile(`^[A-Za-z0-9:._-]{1,128}$`)

type PacketClubAffiliation struct {
	RegistrationID string  `json:"registrationId"`
	PersonID       *string `json:"personId"`
	Club           *string `json:"club"`
}
type PacketClubSnapshot struct {
	SchemaVersion int                     `json:"schemaVersion"`
	ScopeID       string                  `json:"scopeId"`
	Affiliations  []PacketClubAffiliation `json:"affiliations"`
}
type PacketClubOperation struct {
	SchemaVersion    int             `json:"schemaVersion"`
	OperationID      string          `json:"operationId"`
	ScopeID          string          `json:"scopeId"`
	OriginInstanceID string          `json:"originInstanceId"`
	RegistrationID   string          `json:"registrationId"`
	PersonID         json.RawMessage `json:"personId"`
	Club             json.RawMessage `json:"club"`
}
type PacketClubReceipt struct {
	SchemaVersion int     `json:"schemaVersion"`
	OperationID   string  `json:"operationId"`
	Outcome       string  `json:"outcome"`
	Code          *string `json:"code"`
	Known         bool    `json:"known"`
}

func ParsePacketClubOperation(raw []byte) (PacketClubOperation, error) {
	var op PacketClubOperation
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&op) != nil {
		return op, ErrInvalidClubOperation
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return op, ErrInvalidClubOperation
	}
	operationID, err := uuid.Parse(op.OperationID)
	originID, originErr := uuid.Parse(op.OriginInstanceID)
	if err != nil || originErr != nil || operationID.String() != op.OperationID || originID.String() != op.OriginInstanceID || op.SchemaVersion != 1 || op.ScopeID == "" || len(op.ScopeID) > 256 || !clubIdentifier.MatchString(op.RegistrationID) || len(op.PersonID) == 0 || len(op.Club) == 0 {
		return op, ErrInvalidClubOperation
	}
	var person *string
	if json.Unmarshal(op.PersonID, &person) != nil || person != nil && !clubIdentifier.MatchString(*person) {
		return op, ErrInvalidClubOperation
	}
	if _, err := decodeClubMetadata(op.Club); err != nil {
		return op, ErrInvalidClubOperation
	}
	return op, nil
}

func ReadPacketClubAffiliations(ctx context.Context, store *sqlite.Store, connection PacketConnectionContext) (PacketClubSnapshot, error) {
	result := PacketClubSnapshot{SchemaVersion: 1, ScopeID: connection.ScopeID, Affiliations: []PacketClubAffiliation{}}
	err := store.WithinTx(ctx, func(tx *sqlite.Store) error {
		scope, err := tx.GetPacketIssuanceScope(ctx, connection.EventID)
		if err != nil {
			return err
		}
		if scope.ScopeID != connection.ScopeID {
			return ErrClubIdentityConflict
		}
		owners, err := tx.PacketClubOwners(ctx, connection.EventID)
		if err != nil {
			return err
		}
		clubs, err := tx.MemberClubs(ctx, connection.EventID)
		if err != nil {
			return err
		}
		if len(owners) > 20000 {
			return fmt.Errorf("club_affiliation_limit")
		}
		ids := make([]string, 0, len(clubs))
		for id := range clubs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			if owner, exists := owners[id]; exists {
				result.Affiliations = append(result.Affiliations, PacketClubAffiliation{RegistrationID: id, PersonID: owner, Club: clubs[id]})
			}
		}
		return nil
	})
	return result, err
}

func ApplyPacketClubAffiliation(ctx context.Context, store *sqlite.Store, connection PacketConnectionContext, raw []byte) (PacketClubReceipt, error) {
	op, err := ParsePacketClubOperation(raw)
	if err != nil {
		return PacketClubReceipt{}, err
	}
	if op.ScopeID != connection.ScopeID || op.OriginInstanceID != connection.OriginInstanceID {
		return PacketClubReceipt{}, ErrClubIdentityConflict
	}
	encoded, err := json.Marshal(op)
	if err != nil {
		return PacketClubReceipt{}, err
	}
	digest := sha256.Sum256(encoded)
	hash := hex.EncodeToString(digest[:])
	receipt := PacketClubReceipt{SchemaVersion: 1, OperationID: op.OperationID, Outcome: "applied"}
	err = store.WithinTx(ctx, func(tx *sqlite.Store) error {
		scope, err := tx.GetPacketIssuanceScope(ctx, connection.EventID)
		if err != nil {
			return err
		}
		if scope.ScopeID != connection.ScopeID {
			return ErrClubIdentityConflict
		}
		previous, err := tx.FindPacketClubEditReceipt(ctx, op.OperationID)
		if err != nil {
			return err
		}
		if previous != nil {
			if previous.EventID != connection.EventID || previous.ScopeID != connection.ScopeID || previous.OriginInstanceID != connection.OriginInstanceID || previous.ContentHash != hash {
				return ErrClubIdentityConflict
			}
			if err = json.Unmarshal(previous.ReceiptJSON, &receipt); err != nil {
				return err
			}
			receipt.Known = true
			return nil
		}
		registration, err := tx.FindPacketRegistration(ctx, connection.EventID, op.RegistrationID)
		if err != nil {
			return err
		}
		club, err := decodeClubMetadata(op.Club)
		if err != nil {
			return err
		}
		var owner *string
		if err = json.Unmarshal(op.PersonID, &owner); err != nil {
			return err
		}
		if !registration.Found || registration.Deleted {
			code := "registration_not_found"
			receipt.Outcome = "rejected"
			receipt.Code = &code
		} else if registration.Value.Person == nil && (owner != nil || club != nil) || registration.Value.Person != nil && (owner == nil || *owner != registration.Value.Person.ID) {
			code := "club_owner_changed"
			receipt.Outcome = "conflict"
			receipt.Code = &code
		} else if _, err = ApplyEdit(ctx, tx, EditRequest{Entity: "member", EntityID: op.RegistrationID, Field: "club", Value: op.Club}); err != nil {
			return err
		}
		saved, err := json.Marshal(receipt)
		if err != nil {
			return err
		}
		return tx.InsertPacketClubEditReceipt(ctx, sqlite.PacketClubEditReceipt{OperationID: op.OperationID, ConnectionID: connection.ConnectionID, EventID: connection.EventID, ScopeID: connection.ScopeID, OriginInstanceID: connection.OriginInstanceID, ContentHash: hash, OperationJSON: encoded, ReceiptJSON: saved})
	})
	return receipt, err
}
