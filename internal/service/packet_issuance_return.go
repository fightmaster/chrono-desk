package service

import (
	"context"
	"database/sql"
	"errors"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/packetissuance"
)

func (r *PacketIssuanceReceiver) receiveReserveReturn(ctx context.Context, store *sqlite.Store, operation packetissuance.Operation, hash string, known bool, receipt *packetissuance.Receipt) error {
	command := operation.Command
	eventID := operation.Changes[0].Before.EventID
	code, err := store.CheckPacketNumber(ctx, eventID, command.RegistrationID, command.Bib)
	if err != nil {
		return err
	}
	if code != "" {
		return r.persist(ctx, store, operation, hash, "conflict", code, nil, known, receipt)
	}
	race, err := store.GetRace(ctx, command.RaceID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && race.EventID != eventID {
		return r.persist(ctx, store, operation, hash, "rejected", "invalid_target", nil, known, receipt)
	}
	if err != nil {
		return err
	}
	current, err := store.GetPacketRegistrations(ctx, eventID, []string{command.RegistrationID})
	if errors.Is(err, sql.ErrNoRows) {
		return r.persist(ctx, store, operation, hash, "rejected", "registration_not_found", nil, known, receipt)
	}
	if err != nil {
		return err
	}
	_, err = store.GetMember(ctx, command.TargetID)
	if err == nil {
		return r.persist(ctx, store, operation, hash, "conflict", "registration_already_exists", nil, known, receipt)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	empty, err := packetissuance.EmptyReserve(command, current[0])
	if err != nil {
		return err
	}
	current = append(current, empty)
	decision, code, changes := packetMergeDecision(current, operation, store, ctx)
	if decision == "applied" {
		member, err := store.GetMember(ctx, command.RegistrationID)
		if err != nil {
			return err
		}
		if err := store.PutPacketRegistration(ctx, changes[0].After, []string{operation.OperationID}, member.CategoryID); err != nil {
			return err
		}
		if err := store.RestorePacketRFID(ctx, command.RegistrationID, nil); err != nil {
			return err
		}
		if err := store.InsertPacketRegistration(ctx, changes[1].After, []string{operation.OperationID}, nil); err != nil {
			return err
		}
		if err := store.RestorePacketRFID(ctx, command.TargetID, member.RFID); err != nil {
			return err
		}
	}
	return r.persist(ctx, store, operation, hash, decision, code, changes, known, receipt)
}
