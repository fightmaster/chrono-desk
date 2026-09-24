package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/packetissuance"
)

func TestPacketUnassignPreservesParticipantAndReplicates(t *testing.T) {
	data, err := os.ReadFile("../packetissuance/testdata/packet-issuance-unassign-number.json")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != "48f8870974e0e9d20d1ef451895ed823f63911c5cfb63e747785dac69d577c6b" {
		t.Fatal("unassign fixture drift")
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
	producer, consumer := packetStore(t, op), packetStore(t, op)
	ctx := context.Background()
	connection := PacketConnectionContext{ConnectionID: "tablet-a", EventID: "621632", ScopeID: op.ScopeID, OriginInstanceID: op.OriginInstanceID}
	before, err := producer.GetMember(ctx, "700")
	if err != nil {
		t.Fatal(err)
	}
	receiver := NewPacketIssuanceReceiver()
	for attempt := 0; attempt < 2; attempt++ {
		receipts, err := receiver.Receive(ctx, producer, connection, []packetissuance.Operation{op})
		if err != nil || len(receipts) != 1 || receipts[0].Outcome != "applied" || receipts[0].Known != (attempt == 1) {
			t.Fatalf("receipts=%+v err=%v", receipts, err)
		}
	}
	after, err := producer.GetMember(ctx, "700")
	if err != nil {
		t.Fatal(err)
	}
	before.Number = nil
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("participant changed: before=%+v after=%+v", before, after)
	}
	rows, err := producer.ListPacketRegistrations(ctx, "621632")
	if err != nil || len(rows) != 1 || !reflect.DeepEqual(rows[0], op.Changes[0].After) {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	page, err := PacketIssuanceFeedPage(ctx, producer, "621632", op.ScopeID, "0", 100)
	if err != nil {
		t.Fatal(err)
	}
	applications, err := ApplyPacketFeedPage(ctx, consumer, "621632", page)
	if err != nil || len(applications) != 1 || applications[0].Application != "applied" {
		t.Fatalf("feed=%+v err=%v", applications, err)
	}
	mirrored, err := consumer.GetMember(ctx, "700")
	if err != nil || !reflect.DeepEqual(after, mirrored) {
		t.Fatalf("mirror=%+v err=%v", mirrored, err)
	}
	timing := op.Changes[0].Before
	timing.HasTimingEvidence = true
	if _, err := packetissuance.Apply([]packetissuance.Registration{timing}, op.Command); err == nil || err.Error() != "timing_review_required" {
		t.Fatalf("timing guard=%v", err)
	}
}
