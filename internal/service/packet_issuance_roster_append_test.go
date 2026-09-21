package service

import (
	"context"
	"encoding/json"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/packetissuance"
)

func TestSiteRosterAppendPreservesLocalParticipantAndRelaysCreation(t *testing.T) {
	store, operation := preparedPacketFeedStore(t)
	ctx := context.Background()
	local := operation.Changes[0].Before
	local.Issued = true
	if err := store.PutPacketRegistration(ctx, local, nil, nil); err != nil {
		t.Fatal(err)
	}
	added := local
	added.ID, added.Bib, added.EPC, added.Issued = "799", "131", "NEW131", false
	encoded, err := json.Marshal(added)
	if err != nil {
		t.Fatal(err)
	}
	var after map[string]any
	if err := json.Unmarshal(encoded, &after); err != nil {
		t.Fatal(err)
	}
	delete(after, "hasTimingEvidence")
	scopeID := "site:22222222-2222-4222-8222-222222222222:621632"
	wire, err := json.Marshal(map[string]any{
		"schemaVersion": 1, "scopeId": scopeID,
		"cursor": map[string]any{"after": "16", "next": "17", "head": "17", "hasMore": false},
		"actions": []any{map[string]any{
			"actionId": "99999999-9999-4999-8999-999999999999", "kind": "server_change", "sequence": "17",
			"recordedAt": "2026-09-22T08:00:00.000Z", "sourceCode": "admin.roster_append", "outcome": "applied",
			"code": nil, "operation": nil, "changes": []any{map[string]any{"registrationId": added.ID, "before": nil, "after": after}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := packetissuance.ParseFeedPage(wire, scopeID, "16")
	if err != nil {
		t.Fatal(err)
	}
	applications, err := ApplyPacketFeedPage(ctx, store, "621632", page)
	if err != nil || len(applications) != 1 || applications[0].Application != "applied" {
		t.Fatalf("applications=%+v err=%v", applications, err)
	}
	rows, err := store.ListPacketRegistrations(ctx, "621632")
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	for _, row := range rows {
		if row.ID == "700" && !row.Issued {
			t.Fatal("local issuance was lost")
		}
		if row.ID == added.ID && (row.Bib != "131" || row.Issued) {
			t.Fatalf("incorrect new registration: %+v", row)
		}
	}
	lan, err := PacketIssuanceFeedPage(ctx, store, "621632", scopeID, "0", 100)
	if err != nil || len(lan.Actions) != 1 || len(lan.Actions[0].Changes) != 1 ||
		lan.Actions[0].Changes[0].Before != nil || lan.Actions[0].Changes[0].After.ID != added.ID {
		t.Fatalf("creation not relayed to tablets: %+v err=%v", lan, err)
	}
}
