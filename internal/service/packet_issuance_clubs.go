package service

import (
	"context"
	"fmt"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/packetissuance"
)

// Capture all source affiliation before projecting either side of an atomic move.
func packetClubProjection(ctx context.Context, store *sqlite.Store, eventID string, changes []packetissuance.Change) (map[string]*string, error) {
	ids := make([]string, 0, len(changes))
	for _, change := range changes {
		ids = append(ids, change.RegistrationID)
	}
	existing, err := store.MemberClubsForIDs(ctx, eventID, ids)
	if err != nil {
		return nil, err
	}
	sources := map[string]string{}
	for _, change := range changes {
		if change.Before.Person == nil {
			continue
		}
		personID := change.Before.Person.ID
		if _, exists := sources[personID]; exists {
			sources[personID] = ""
		} else {
			sources[personID] = change.RegistrationID
		}
	}
	result := map[string]*string{}
	for _, change := range changes {
		if change.After.EventID != eventID {
			return nil, fmt.Errorf("club projection event mismatch")
		}
		before, after := change.Before.Person, change.After.Person
		if after == nil {
			result[change.RegistrationID] = nil
			continue
		}
		if before != nil && before.ID == after.ID {
			if club, known := existing[change.RegistrationID]; known {
				result[change.RegistrationID] = club
			}
			continue
		}
		result[change.RegistrationID] = nil // A new person never inherits the old owner's Club.
		if source := sources[after.ID]; source != "" && source != change.RegistrationID {
			result[change.RegistrationID] = existing[source]
		}
	}
	return result, nil
}

// Derived affiliation follows its audited canonical command/feed. It is not a
// new local edit: echoing a copied stale Club back to the site would overwrite
// a newer authoritative affiliation for the same person.
func applyPacketClubs(ctx context.Context, store *sqlite.Store, eventID string, clubs map[string]*string) error {
	_, err := store.ApplyMemberClubs(ctx, eventID, clubs)
	return err
}
