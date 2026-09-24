package service

import (
	"context"
	"database/sql"
	"errors"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/packetissuance"
)

func (r *PacketIssuanceReceiver) receiveNumber(ctx context.Context, store *sqlite.Store, operation packetissuance.Operation, hash string, known bool, receipt *packetissuance.Receipt) error {
	command := operation.Command
	eventID := operation.Changes[0].Before.EventID
	creating := command.Type == "create_registration"
	code, err := store.CheckPacketNumber(ctx, eventID, command.RegistrationID, command.Bib)
	if err != nil {
		return err
	}
	if code != "" {
		return r.persist(ctx, store, operation, hash, "conflict", code, nil, known, receipt)
	}
	var current []packetissuance.Registration
	if creating {
		_, err := store.GetMember(ctx, command.RegistrationID)
		if err == nil {
			return r.persist(ctx, store, operation, hash, "conflict", "registration_already_exists", nil, known, receipt)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		race, err := store.GetRace(ctx, command.RaceID)
		if errors.Is(err, sql.ErrNoRows) || err == nil && race.EventID != eventID {
			return r.persist(ctx, store, operation, hash, "rejected", "invalid_target", nil, known, receipt)
		}
		if err != nil {
			return err
		}
		if command.Person.BirthDate == "" || command.Person.Gender == "" {
			return r.persist(ctx, store, operation, hash, "rejected", "person_details_required", nil, known, receipt)
		}
		empty, err := packetissuance.EmptyRegistration(command)
		if err != nil {
			return err
		}
		current = []packetissuance.Registration{empty}
	} else {
		current, err = store.GetPacketRegistrations(ctx, eventID, []string{command.RegistrationID})
		if errors.Is(err, sql.ErrNoRows) {
			return r.persist(ctx, store, operation, hash, "rejected", "registration_not_found", nil, known, receipt)
		}
		if err != nil {
			return err
		}
		if current[0].HasTimingEvidence {
			return r.persist(ctx, store, operation, hash, "conflict", "timing_review_required", nil, known, receipt)
		}
	}
	decision, code, changes := packetMergeDecision(current, operation, store, ctx)
	if decision == "applied" {
		row := changes[0].After
		if creating {
			category, err := resolveCategoryIDForMember(ctx, store, packetMember(row))
			if err != nil {
				return err
			}
			if err := store.InsertPacketRegistration(ctx, row, []string{operation.OperationID}, category); err != nil {
				return err
			}
		} else {
			member, err := store.GetMember(ctx, row.ID)
			if err != nil {
				return err
			}
			if err := store.PutPacketRegistration(ctx, row, []string{operation.OperationID}, member.CategoryID); err != nil {
				return err
			}
		}
	}
	return r.persist(ctx, store, operation, hash, decision, code, changes, known, receipt)
}
