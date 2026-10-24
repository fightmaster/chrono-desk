package service

import (
	"context"
	"encoding/json"
	"gitlab.com/fightmaster1/chrono-desk/internal/packetissuance"
	"testing"
)

func TestClubEditIsVisibleEverywhereWithoutChangingTimingOrPacketHeads(t *testing.T) {
	store := newTestStore(t)
	importFixture(t, store)
	ctx := context.Background()
	before, err := store.LiveFinishRankRevision(ctx, "ev-100")
	if err != nil {
		t.Fatal(err)
	}
	result, err := ApplyEdit(ctx, store, EditRequest{Entity: "member", EntityID: "mem-1", Field: "club", Value: json.RawMessage(`"  Club A  "`)})
	if err != nil {
		t.Fatal(err)
	}
	if result.RecountNeeded {
		t.Fatal("Club edit requests recount")
	}
	member, err := store.GetMember(ctx, "mem-1")
	if err != nil {
		t.Fatal(err)
	}
	if member.Club == nil || *member.Club != "Club A" {
		t.Fatalf("Club not exposed: %+v", member.Club)
	}
	after, _ := store.LiveFinishRankRevision(ctx, "ev-100")
	if before != after {
		t.Fatal("Club changed ranking revision")
	}
	passes, err := LoadMemberPasses(ctx, store, "mem-1")
	if err != nil || passes.Registration.Club == nil || *passes.Registration.Club != "Club A" {
		t.Fatalf("drawer Club missing: %v", err)
	}
	changes, _ := store.ListLocalChanges(ctx)
	if len(changes) != 1 || changes[0].Field != "club" {
		t.Fatal("Club edit not journaled", changes)
	}
	if _, err := ApplyEdit(ctx, store, EditRequest{Entity: "member", EntityID: "mem-1", Field: "club", Value: json.RawMessage(`null`)}); err != nil {
		t.Fatal(err)
	}
	clubs, _ := store.MemberClubs(ctx, "ev-100")
	if club, known := clubs["mem-1"]; !known || club != nil {
		t.Fatal("explicit clear lost", clubs)
	}
	for _, raw := range []string{`1`, `"bad\u0000club"`} {
		if _, err := ApplyEdit(ctx, store, EditRequest{Entity: "member", EntityID: "mem-1", Field: "club", Value: json.RawMessage(raw)}); err == nil {
			t.Fatal("invalid Club accepted")
		}
	}
}

func TestClubCreationKeepsIndependentTeamAndV3ExplicitClear(t *testing.T) {
	store := newTestStore(t)
	importFixture(t, store)
	ctx := context.Background()
	dob := "2000-01-01"
	team := "Relay team"
	id, result, err := CreateMember(ctx, store, "ev-100", CreateMemberRequest{RaceID: "race-10k", FirstName: "Synthetic", LastName: "Runner", DOB: &dob, Team: &team, Club: json.RawMessage(`"Club A"`)})
	if err != nil || result.RecountNeeded {
		t.Fatal(err, result)
	}
	member, err := store.GetMember(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if member.Club == nil || *member.Club != "Club A" || member.Team == nil || *member.Team != team {
		t.Fatal("independent fields lost")
	}
	if _, err = ApplyEdit(ctx, store, EditRequest{Entity: "member", EntityID: id, Field: "club", Value: json.RawMessage(`null`)}); err != nil {
		t.Fatal(err)
	}
	payload, _, err := BuildSyncPayloadV3(ctx, store, "ev-100", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Members []map[string]json.RawMessage `json:"new_members"`
	}
	if err = json.Unmarshal(payload, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Members) != 1 || string(doc.Members[0]["club"]) != "null" || string(doc.Members[0]["team"]) != `"Relay team"` {
		t.Fatalf("explicit clear lost in creation sync: %s", payload)
	}
	if _, _, err := BuildSyncPayload(ctx, store, "ev-100", true); err == nil {
		t.Fatal("legacy builder silently discarded an explicit Club clear")
	}
}

func TestClubOutboxAndReplayRetainOriginalPacketOwner(t *testing.T) {
	op := packetFixtureOperation(t)
	store := packetStore(t, op)
	ctx := context.Background()
	row := op.Changes[0].Before
	if _, err := ApplyEdit(ctx, store, EditRequest{Entity: "member", EntityID: row.ID, Field: "club", Value: json.RawMessage(`"Club A"`)}); err != nil {
		t.Fatal(err)
	}
	payload, _, err := BuildSyncPayloadV3(ctx, store, row.EventID, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Edits []struct {
			Reference struct {
				Owner string `json:"club_person_id"`
			} `json:"member_ref"`
		} `json:"member_edits"`
	}
	if err = json.Unmarshal(payload, &doc); err != nil || len(doc.Edits) != 1 || doc.Edits[0].Reference.Owner != row.Person.ID {
		t.Fatalf("owner missing: %s, %v", payload, err)
	}
	replacement := *row.Person
	replacement.ID = "replacement-person"
	row.Person = &replacement
	if err = store.PutPacketRegistration(ctx, row, nil, nil); err != nil {
		t.Fatal(err)
	}
	club := "Replacement Club"
	if _, err = store.ApplyMemberClubs(ctx, row.EventID, map[string]*string{row.ID: &club}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = BuildSyncPayloadV3(ctx, store, row.EventID, true, nil); err == nil {
		t.Fatal("stale owner Club edit was rebound to replacement")
	}
	if _, err = ReapplyLocalEdits(ctx, store); err != nil {
		t.Fatal(err)
	}
	member, err := store.GetMember(ctx, row.ID)
	if err != nil || member.Club == nil || *member.Club != club {
		t.Fatal("replay overwrote replacement Club", err)
	}
	for _, owners := range []map[string]*string{nil, {row.ID: &op.Changes[0].Before.Person.ID}} {
		wrong := "Stale import"
		if n, err := store.ApplyBoundMemberClubs(ctx, row.EventID, map[string]*string{row.ID: &wrong}, owners); err != nil || n != 0 {
			t.Fatal("old or mismatched import accepted", n, err)
		}
	}
	next := "Fresh Club"
	if n, err := store.ApplyBoundMemberClubs(ctx, row.EventID, map[string]*string{row.ID: &next}, map[string]*string{row.ID: &replacement.ID}); err != nil || n != 1 {
		t.Fatal("current owner import rejected", n, err)
	}
}

func TestClubProjectionMovesPersonHistoryAndClearsReplacementWithoutEchoEdits(t *testing.T) {
	op := packetFixtureOperation(t)
	store := packetStore(t, op)
	ctx := context.Background()
	source := op.Changes[0].Before
	a, b := "A historical club", "B historical club"
	if _, err := store.ApplyMemberClubs(ctx, source.EventID, map[string]*string{source.ID: &a}); err != nil {
		t.Fatal(err)
	}
	// A move's target was a different owner; only the source's affiliation follows.
	target := source
	target.ID = "target"
	other := *source.Person
	other.ID = "old-target-person"
	target.Person = &other
	targetAfter := target
	targetAfter.Person = source.Person
	cleared := source
	cleared.Person = nil
	changes := []packetissuance.Change{{RegistrationID: source.ID, Before: source, After: cleared}, {RegistrationID: target.ID, Before: target, After: targetAfter}}
	projected, err := packetClubProjection(ctx, store, source.EventID, changes)
	if err != nil || projected[source.ID] != nil || projected[target.ID] == nil || *projected[target.ID] != a {
		t.Fatal("move lost source Club", projected, err)
	}
	replacement := source
	replacement.Person = &other
	projected, err = packetClubProjection(ctx, store, source.EventID, []packetissuance.Change{{RegistrationID: source.ID, Before: source, After: replacement}})
	if err != nil || projected[source.ID] != nil {
		t.Fatal("replacement inherited Club", err)
	}
	if err = applyPacketClubs(ctx, store, source.EventID, map[string]*string{source.ID: &b}); err != nil {
		t.Fatal(err)
	}
	journal, err := store.ListLocalChanges(ctx)
	if err != nil || len(journal) != 0 {
		t.Fatal("derived feed projection echoed a new local edit", journal, err)
	}
}
