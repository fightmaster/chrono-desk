package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/packetissuance"
)

func TestPacketUnnumberedCreationPersistsRetriesAndReplicates(t *testing.T) {
	data, err := os.ReadFile("../packetissuance/testdata/packet-issuance-unnumbered-create.json")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != "417df266bd5d80506e247a4608ebfe21323c13615bb3594a62f7676331437cd1" {
		t.Fatal("unnumbered fixture drift")
	}
	var fixture struct {
		Operations []json.RawMessage `json:"operations"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	op, err := packetissuance.ParseOperation(fixture.Operations[0])
	if err != nil {
		t.Fatal(err)
	}
	seed := numberOperations(t)[0]
	producer, consumer := packetStore(t, seed), packetStore(t, seed)
	ctx := context.Background()
	connection := PacketConnectionContext{ConnectionID: "tablet-a", EventID: "621632", ScopeID: op.ScopeID, OriginInstanceID: op.OriginInstanceID}
	receiver := NewPacketIssuanceReceiver()
	for attempt := 0; attempt < 2; attempt++ {
		receipts, err := receiver.Receive(ctx, producer, connection, []packetissuance.Operation{op})
		if err != nil || len(receipts) != 1 || receipts[0].Outcome != "applied" || receipts[0].Known != (attempt == 1) {
			t.Fatalf("receipts=%+v err=%v", receipts, err)
		}
	}
	page, err := PacketIssuanceFeedPage(ctx, producer, "621632", op.ScopeID, "0", 100)
	if err != nil {
		t.Fatal(err)
	}
	applications, err := ApplyPacketFeedPage(ctx, consumer, "621632", page)
	if err != nil || len(applications) != 1 || applications[0].Application != "applied" {
		t.Fatalf("feed=%+v err=%v", applications, err)
	}
	for _, store := range []*sqlite.Store{producer, consumer} {
		member, err := store.GetMember(ctx, op.Command.RegistrationID)
		if err != nil || member.Number != nil || member.FirstName != "Анна" || member.RaceID != "100" {
			t.Fatalf("member=%+v err=%v", member, err)
		}
	}
	issued := true
	command := op.Command
	command.IssuePacket = &issued
	if _, err := packetissuance.Apply([]packetissuance.Registration{op.Changes[0].Before}, command); err == nil {
		t.Fatal("unnumbered issued packet accepted")
	}
}
