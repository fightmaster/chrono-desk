package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

// Packet-scope Club edits share the durable detached relay channel. Timing-only
// event editors keep their existing generic v3 member-edit path.
func applyNativeClubEdit(ctx context.Context, store *sqlite.Store, req EditRequest) (EditResult, error) {
	var result EditResult
	err := store.WithinTx(ctx, func(tx *sqlite.Store) error {
		member, err := tx.GetMember(ctx, req.EntityID)
		if err != nil {
			return err
		}
		scope, err := tx.GetPacketIssuanceScope(ctx, member.EventID)
		if err != nil {
			return err
		}
		if scope.ScopeID == "" {
			result, err = applyEdit(ctx, newSQLiteEditStore(tx), req)
			return err
		}
		origin := tx.ClubOriginInstanceID()
		if !packetRelayUUID.MatchString(origin) {
			return fmt.Errorf("не настроена идентичность Chrono Desk для отправки клуба")
		}
		registration, err := tx.FindPacketRegistration(ctx, member.EventID, member.ID)
		if err != nil {
			return err
		}
		if !registration.Found || registration.Deleted {
			return fmt.Errorf("участник отсутствует в журнале выдачи")
		}
		club, err := decodeClubMetadata(req.Value)
		if err != nil {
			return err
		}
		var personID *string
		if registration.Value.Person != nil {
			personID = &registration.Value.Person.ID
		} else if club != nil {
			return fmt.Errorf("у пустого резерва не может быть клуба")
		}
		var expected *string
		if len(req.ClubPersonID) == 0 || json.Unmarshal(req.ClubPersonID, &expected) != nil ||
			(expected == nil) != (personID == nil) || expected != nil && *expected != *personID {
			return fmt.Errorf("владелец номера изменился; откройте карточку заново перед изменением клуба")
		}
		personJSON, err := json.Marshal(personID)
		if err != nil {
			return err
		}
		operation := PacketClubOperation{SchemaVersion: 1, OperationID: uuid.NewString(), ScopeID: scope.ScopeID, OriginInstanceID: origin, RegistrationID: member.ID, PersonID: personJSON, Club: req.Value}
		body, err := json.Marshal(operation)
		if err != nil {
			return err
		}
		result, err = applyEdit(ctx, newSQLiteEditStore(tx), req)
		if err != nil {
			return err
		}
		return tx.EnqueuePacketClubEdit(ctx, member.EventID, scope.ScopeID, member.ID, operation.OperationID, body, registration.Heads)
	})
	return result, err
}
