package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestPacketClubReceiptIsAtomicAndOwnerBoundAcrossRetry(t *testing.T) {
	op := packetFixtureOperation(t)
	store := packetStore(t, op)
	ctx := context.Background()
	row := op.Changes[0].Before
	connection := PacketConnectionContext{ConnectionID: "club-connection", EventID: row.EventID, ScopeID: op.ScopeID, OriginInstanceID: op.OriginInstanceID}
	document := map[string]any{"schemaVersion": 1, "operationId": "99999999-9999-4999-8999-999999999999", "scopeId": op.ScopeID, "originInstanceId": op.OriginInstanceID, "registrationId": row.ID, "personId": row.Person.ID, "club": "  Club A  "}
	raw, _ := json.Marshal(document)
	before, _ := store.FindPacketRegistration(ctx, row.EventID, row.ID)
	first, err := ApplyPacketClubAffiliation(ctx, store, connection, raw)
	if err != nil || first.Outcome != "applied" || first.Known {
		t.Fatal(first, err)
	}
	retry, err := ApplyPacketClubAffiliation(ctx, store, connection, raw)
	if err != nil || !retry.Known || retry.Outcome != "applied" {
		t.Fatal(retry, err)
	}
	member, err := store.GetMember(ctx, row.ID)
	if err != nil || member.Club == nil || *member.Club != "Club A" {
		t.Fatal(member.Club, err)
	}
	after, _ := store.FindPacketRegistration(ctx, row.EventID, row.ID)
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if string(a) != string(b) {
		t.Fatal("Club changed canonical record/heads")
	}
	journal, _ := store.ListLocalChanges(ctx)
	if len(journal) != 1 || journal[0].Field != "club" {
		t.Fatal("retry duplicated audit", journal)
	}
	document["club"] = "UUID reused with another Club"
	different, _ := json.Marshal(document)
	if _, err = ApplyPacketClubAffiliation(ctx, store, connection, different); !errors.Is(err, ErrClubIdentityConflict) {
		t.Fatal(err)
	}
	other := *row.Person
	other.ID = "replacement"
	row.Person = &other
	if err = store.PutPacketRegistration(ctx, row, nil, nil); err != nil {
		t.Fatal(err)
	}
	document["operationId"] = "88888888-8888-4888-8888-888888888888"
	stale, _ := json.Marshal(document)
	conflict, err := ApplyPacketClubAffiliation(ctx, store, connection, stale)
	if err != nil || conflict.Outcome != "conflict" || conflict.Code == nil || *conflict.Code != "club_owner_changed" {
		t.Fatal(conflict, err)
	}
	row.Person = op.Changes[0].Before.Person
	if err = store.PutPacketRegistration(ctx, row, nil, nil); err != nil {
		t.Fatal(err)
	}
	conflict, err = ApplyPacketClubAffiliation(ctx, store, connection, stale)
	if err != nil || conflict.Outcome != "conflict" || !conflict.Known {
		t.Fatal("terminal conflict was resurrected", conflict, err)
	}
	snapshot, err := ReadPacketClubAffiliations(ctx, store, connection)
	if err != nil || len(snapshot.Affiliations) != 1 || *snapshot.Affiliations[0].Club != "Club A" {
		t.Fatal(snapshot, err)
	}
	document["operationId"] = "77777777-7777-4777-8777-777777777777"
	document["club"] = nil
	clear, _ := json.Marshal(document)
	if _, err = ApplyPacketClubAffiliation(ctx, store, connection, clear); err != nil {
		t.Fatal(err)
	}
	member, _ = store.GetMember(ctx, row.ID)
	if member.Club != nil {
		t.Fatal("explicit clear lost")
	}
}

func TestPacketClubReceiptFailureRollsBackDisplayAndJournal(t *testing.T) {
	op := packetFixtureOperation(t)
	store := packetStore(t, op)
	ctx := context.Background()
	row := op.Changes[0].Before
	connection := PacketConnectionContext{EventID: row.EventID, ScopeID: op.ScopeID, OriginInstanceID: op.OriginInstanceID}
	if _, err := store.DB().Exec(`CREATE TRIGGER reject_club_receipt BEFORE INSERT ON packet_club_edit_receipts BEGIN SELECT RAISE(FAIL,'synthetic receipt failure'); END`); err != nil {
		t.Fatal(err)
	}
	document := map[string]any{"schemaVersion": 1, "operationId": "99999999-9999-4999-8999-999999999999", "scopeId": op.ScopeID, "originInstanceId": op.OriginInstanceID, "registrationId": row.ID, "personId": row.Person.ID, "club": "Must roll back"}
	raw, _ := json.Marshal(document)
	if _, err := ApplyPacketClubAffiliation(ctx, store, connection, raw); err == nil {
		t.Fatal("failed receipt was acknowledged")
	}
	member, _ := store.GetMember(ctx, row.ID)
	if member.Club != nil {
		t.Fatal("display did not roll back")
	}
	journal, _ := store.ListLocalChanges(ctx)
	if len(journal) != 0 {
		t.Fatal("audit did not roll back")
	}
	for _, bad := range []string{`{}`, `{"schemaVersion":1}`, string(raw) + `{}`, `[]`} {
		if _, err := ParsePacketClubOperation([]byte(bad)); !errors.Is(err, ErrInvalidClubOperation) {
			t.Fatal("invalid accepted", err)
		}
	}
}
