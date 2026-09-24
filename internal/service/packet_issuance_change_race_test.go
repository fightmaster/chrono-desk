package service

import (
	"context"
	"encoding/json"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/packetissuance"
)

func TestPacketRaceChangePersistsAndReplicatesStableRegistration(t *testing.T) {
	base := packetFixtureOperation(t)
	var raw map[string]any
	if err := json.Unmarshal(base.CanonicalJSON(), &raw); err != nil {
		t.Fatal(err)
	}
	raw["command"] = map[string]any{"type": "change_race", "registrationId": base.Command.RegistrationID, "raceId": "101"}
	change := raw["changes"].([]any)[0].(map[string]any)
	before, after := change["before"].(map[string]any), change["after"].(map[string]any)
	before["bib"], before["epc"] = "", ""
	for key, value := range before {
		after[key] = value
	}
	after["raceId"] = "101"
	wire, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	op, err := packetissuance.ParseOperation(wire)
	if err != nil {
		t.Fatal(err)
	}
	producer, consumer := packetStore(t, op), packetStore(t, op)
	ctx := context.Background()
	for _, store := range []*sqlite.Store{producer, consumer} {
		if err := store.UpsertRace(ctx, domain.Race{ID: "101", EventID: op.Changes[0].Before.EventID,
			Name: "10 км", Date: "2026-09-13 10:00:00", Format: domain.FormatFixedDistance}); err != nil {
			t.Fatal(err)
		}
	}
	connection := PacketConnectionContext{ConnectionID: "tablet-a", EventID: op.Changes[0].Before.EventID,
		ScopeID: op.ScopeID, OriginInstanceID: op.OriginInstanceID}
	receiver := NewPacketIssuanceReceiver()
	for attempt := 0; attempt < 2; attempt++ {
		receipts, err := receiver.Receive(ctx, producer, connection, []packetissuance.Operation{op})
		if err != nil || len(receipts) != 1 || receipts[0].Outcome != "applied" || receipts[0].Known != (attempt == 1) {
			t.Fatalf("receipt=%+v err=%v", receipts, err)
		}
	}
	page, err := PacketIssuanceFeedPage(ctx, producer, op.Changes[0].Before.EventID, op.ScopeID, "0", 100)
	if err != nil || len(page.Actions) != 1 || page.Actions[0].Kind != "server_change" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	applications, err := ApplyPacketFeedPage(ctx, consumer, op.Changes[0].Before.EventID, page)
	if err != nil || len(applications) != 1 || applications[0].Application != "applied" {
		t.Fatalf("applications=%+v err=%v", applications, err)
	}
	for _, store := range []*sqlite.Store{producer, consumer} {
		member, err := store.GetMember(ctx, op.Command.RegistrationID)
		if err != nil || member.RaceID != "101" || member.Number != nil || member.FirstName != op.Changes[0].Before.Person.FirstName {
			t.Fatalf("member=%+v err=%v", member, err)
		}
	}
}
