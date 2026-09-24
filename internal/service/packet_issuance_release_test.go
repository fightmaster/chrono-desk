package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/packetissuance"
)

func TestPacketReleaseNumbersPreservesPeopleReplicatesAndRetries(t *testing.T) {
	raw, err := os.ReadFile("../packetissuance/testdata/packet-issuance-release-number.json")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != "1b8b22720cb90091f05f603d2bc11367b259db283172ee82c80f849919c5c840" {
		t.Fatal("fixture drift")
	}
	var fixture struct {
		Operations []json.RawMessage `json:"operations"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, wire := range fixture.Operations {
		op, err := packetissuance.ParseOperation(wire)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(op.OperationID, func(t *testing.T) {
			ctx := context.Background()
			producer, consumer := packetStore(t, op), packetStore(t, op)
			for _, store := range []*sqlite.Store{producer, consumer} {
				if err := store.WithinTx(ctx, func(tx *sqlite.Store) error {
					origins := []packetissuance.ReserveOrigin{}
					if op.Command.Type == "return_to_reserve" {
						origins = append(origins, packetissuance.ReserveOrigin{RegistrationID: "700", Bib: "274", EPC: op.Changes[0].Before.EPC, RaceID: "100"})
					}
					if err := tx.SavePacketReserveOrigins(ctx, "621632", &origins); err != nil {
						return err
					}
					if op.Command.Type == "assign_reserve" {
						if err := tx.InsertPacketRegistration(ctx, op.Changes[1].Before, nil, nil); err != nil {
							return err
						}
						return tx.PutPacketSiteRegistration(ctx, "621632", &op.Changes[1].Before)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			connection := PacketConnectionContext{ConnectionID: "tablet", EventID: "621632", ScopeID: op.ScopeID, OriginInstanceID: op.OriginInstanceID}
			receiver := NewPacketIssuanceReceiver()
			for attempt := 0; attempt < 2; attempt++ {
				receipts, err := receiver.Receive(ctx, producer, connection, []packetissuance.Operation{op})
				if err != nil || len(receipts) != 1 || receipts[0].Outcome != "applied" || receipts[0].Known != (attempt == 1) {
					t.Fatalf("receipt=%+v err=%v", receipts, err)
				}
			}
			rows, err := producer.ListPacketRegistrations(ctx, "621632")
			if err != nil {
				t.Fatal(err)
			}
			for _, change := range op.Changes {
				values, err := producer.GetPacketRegistrations(ctx, "621632", []string{change.RegistrationID})
				if err != nil || !reflect.DeepEqual(values[0], change.After) {
					t.Fatalf("projection=%+v want=%+v err=%v", values, change.After, err)
				}
			}
			for _, row := range rows {
				if row.Reserve && row.Bib == "" {
					t.Fatal("blank reserve")
				}
			}
			page, err := PacketIssuanceFeedPage(ctx, producer, "621632", op.ScopeID, "0", 100)
			if err != nil {
				t.Fatal(err)
			}
			if op.Command.Type == "return_to_reserve" && page.Actions[0].Changes[1].Before != nil {
				t.Fatal("created reserve needs null feed before")
			}
			applications, err := ApplyPacketFeedPage(ctx, consumer, "621632", page)
			if err != nil || len(applications) != 1 || applications[0].Application != "applied" {
				t.Fatalf("applications=%+v err=%v", applications, err)
			}
			copied, err := consumer.ListPacketRegistrations(ctx, "621632")
			if err != nil || !reflect.DeepEqual(rows, copied) {
				t.Fatalf("replicated=%+v expected=%+v err=%v", copied, rows, err)
			}
			origins, err := consumer.PacketReserveOrigins(ctx, "621632", copied)
			if err != nil || origins == nil {
				t.Fatalf("origins=%+v err=%v", origins, err)
			}
			if op.Command.Type == "clear_number" && len(*origins) != 0 {
				t.Fatal("manual number became reserve")
			}
			if op.Command.Type == "return_to_reserve" && (len(*origins) != 1 || (*origins)[0].RegistrationID != op.Command.TargetID) {
				t.Fatalf("reserve origin=%+v", *origins)
			}
		})
	}
}
