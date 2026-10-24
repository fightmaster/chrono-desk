package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

type packetClubHTTPError struct{ status int }

func (e packetClubHTTPError) Error() string {
	return fmt.Sprintf("club metadata receiver returned %d", e.status)
}

func PushPacketClubEdit(ctx context.Context, apiBaseURL, credential string, operationJSON []byte) (PacketClubReceipt, error) {
	operation, err := ParsePacketClubOperation(operationJSON)
	if err != nil {
		return PacketClubReceipt{}, err
	}
	endpoint, err := packetRelayActionURL(apiBaseURL, "/club-affiliations")
	if err != nil {
		return PacketClubReceipt{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(operationJSON))
	if err != nil {
		return PacketClubReceipt{}, err
	}
	request.Header.Set("Authorization", "Bearer "+credential)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := syncHTTPClient.Do(request)
	if err != nil {
		return PacketClubReceipt{}, fmt.Errorf("club metadata delivery unavailable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return PacketClubReceipt{}, packetClubHTTPError{status: response.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil {
		return PacketClubReceipt{}, err
	}
	if len(body) > 4096 {
		return PacketClubReceipt{}, fmt.Errorf("club metadata receipt exceeds limit")
	}
	var wire struct {
		SchemaVersion *int            `json:"schemaVersion"`
		OperationID   string          `json:"operationId"`
		Outcome       string          `json:"outcome"`
		Code          json.RawMessage `json:"code"`
		Known         *bool           `json:"known"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || wire.SchemaVersion == nil || *wire.SchemaVersion != 1 || wire.Known == nil || len(wire.Code) == 0 || wire.OperationID != operation.OperationID {
		return PacketClubReceipt{}, fmt.Errorf("invalid club metadata receipt")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return PacketClubReceipt{}, fmt.Errorf("invalid club metadata receipt")
	}
	var code *string
	if err := json.Unmarshal(wire.Code, &code); err != nil {
		return PacketClubReceipt{}, fmt.Errorf("invalid club metadata receipt")
	}
	if wire.Outcome == "applied" && code != nil || wire.Outcome != "applied" && (wire.Outcome != "conflict" && wire.Outcome != "rejected" || code == nil || *code != "club_owner_changed" && *code != "registration_not_found") {
		return PacketClubReceipt{}, fmt.Errorf("invalid club metadata receipt")
	}
	return PacketClubReceipt{SchemaVersion: 1, OperationID: wire.OperationID, Outcome: wire.Outcome, Code: code, Known: *wire.Known}, nil
}

// Drain only the ready FIFO prefix. An unsent creation is a dependency, whereas
// backoff of an already-ready Club blocks a later owner-changing packet command.
func flushPacketClubOutbox(ctx context.Context, store *sqlite.Store, state sqlite.PacketRelayState, result *PacketOperationSyncResult) (bool, error) {
	for drained := 0; drained < 1024; {
		pending, err := store.PendingPacketClubEdits(ctx, state.EventID)
		if err != nil {
			return false, err
		}
		if len(pending) == 0 {
			return false, nil
		}
		for _, row := range pending {
			if row.ScopeID != state.ScopeID {
				return false, ErrClubIdentityConflict
			}
			ready, conflicted, err := store.PacketClubDependencies(ctx, row)
			if err != nil {
				return false, err
			}
			if conflicted {
				if err := store.DeferPacketClubEdit(ctx, row, "club_dependency_conflict", true, time.Now()); err != nil {
					return false, err
				}
				result.ClubReview++
				drained++
				continue
			}
			if !ready {
				return true, nil
			}
			if row.NextAttemptAt > time.Now().UnixMilli() {
				return true, fmt.Errorf("правка клуба ожидает повторной отправки; последующие переносы сохранены в очереди")
			}
			receipt, err := PushPacketClubEdit(ctx, state.APIBaseURL, state.Credential, row.OperationJSON)
			result.ClubAttempted++
			if err != nil {
				review, code := false, "club_network_error"
				var httpError packetClubHTTPError
				if errors.As(err, &httpError) {
					code = fmt.Sprintf("club_http_%d", httpError.status)
					review = httpError.status == 400 || httpError.status == 401 || httpError.status == 403 || httpError.status == 404 || httpError.status == 409 || httpError.status == 413 || httpError.status == 415 || httpError.status == 422
				}
				if saveErr := store.DeferPacketClubEdit(ctx, row, code, review, time.Now()); saveErr != nil {
					return false, saveErr
				}
				if review {
					result.ClubReview++
					drained++
					continue
				}
				return true, err
			}
			if err := store.AcknowledgePacketClubEdit(ctx, row.OperationID, receipt.Outcome, receipt.Code); err != nil {
				return false, err
			}
			if receipt.Outcome == "applied" {
				result.ClubAccepted++
			} else {
				result.ClubReview++
			}
			drained++
		}
	}
	return true, fmt.Errorf("club metadata delivery limit reached; repeat synchronization")
}

// Refresh detached authoritative metadata after delivery. A missing endpoint is
// old-receiver compatibility; malformed/scoped data never replaces known Club.
func refreshPacketRelayClubs(ctx context.Context, store *sqlite.Store, state sqlite.PacketRelayState) error {
	endpoint, err := packetRelayActionURL(state.APIBaseURL, "/club-affiliations")
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+state.Credential)
	request.Header.Set("Accept", "application/json")
	response, err := syncHTTPClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return packetClubHTTPError{status: response.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (16<<20)+1))
	if err != nil {
		return err
	}
	if len(body) > 16<<20 {
		return fmt.Errorf("club metadata snapshot exceeds limit")
	}
	var wire struct {
		SchemaVersion *int   `json:"schemaVersion"`
		ScopeID       string `json:"scopeId"`
		Affiliations  *[]struct {
			RegistrationID string          `json:"registrationId"`
			PersonID       json.RawMessage `json:"personId"`
			Club           json.RawMessage `json:"club"`
		} `json:"affiliations"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || wire.SchemaVersion == nil || *wire.SchemaVersion != 1 || wire.ScopeID != state.ScopeID || wire.Affiliations == nil || len(*wire.Affiliations) > 20000 {
		return fmt.Errorf("invalid club metadata snapshot")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return fmt.Errorf("invalid club metadata snapshot")
	}
	clubs, owners := map[string]*string{}, map[string]*string{}
	for _, row := range *wire.Affiliations {
		if _, exists := clubs[row.RegistrationID]; exists || !clubIdentifier.MatchString(row.RegistrationID) || len(row.Club) == 0 || len(row.PersonID) == 0 {
			return fmt.Errorf("invalid club metadata snapshot")
		}
		var owner *string
		if json.Unmarshal(row.PersonID, &owner) != nil || owner != nil && !clubIdentifier.MatchString(*owner) {
			return fmt.Errorf("invalid club metadata snapshot")
		}
		club, err := decodeClubMetadata(row.Club)
		if err != nil {
			return err
		}
		if owner == nil && club != nil {
			return fmt.Errorf("invalid club metadata snapshot")
		}
		clubs[row.RegistrationID], owners[row.RegistrationID] = club, owner
	}
	return store.WithinTx(ctx, func(tx *sqlite.Store) error {
		scope, err := tx.GetPacketIssuanceScope(ctx, state.EventID)
		if err != nil {
			return err
		}
		if scope.ScopeID != state.ScopeID {
			return ErrClubIdentityConflict
		}
		_, err = tx.ApplyBoundMemberClubs(ctx, state.EventID, clubs, owners)
		return err
	})
}
