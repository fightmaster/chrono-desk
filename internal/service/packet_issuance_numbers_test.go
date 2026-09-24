package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"log"
	"os"
	"path/filepath"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/packetissuance"
)

func numberOperations(t *testing.T) []packetissuance.Operation {
	t.Helper()
	data, err := os.ReadFile("../packetissuance/testdata/packet-issuance-numbers.json")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != "b56b78ce98dd182b48302e6ba71f9a5bd1b8de9cfe1d32d7ca8016088304106b" {
		t.Fatal("canonical number-assignment fixture drift")
	}
	var fixture struct {
		Operations []json.RawMessage `json:"operations"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	operations := make([]packetissuance.Operation, len(fixture.Operations))
	for i, raw := range fixture.Operations {
		operations[i], err = packetissuance.ParseOperation(raw)
		if err != nil {
			t.Fatal(err)
		}
	}
	return operations
}

func TestPacketNumberAssignmentAndCreationPersistAndRetry(t *testing.T) {
	operations := numberOperations(t)
	store := packetStore(t, operations[0])
	if _, err := store.DB().Exec(`UPDATE members SET number=NULL`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	receiver := NewPacketIssuanceReceiver()
	connection := PacketConnectionContext{ConnectionID: "tablet-a", EventID: "621632", ScopeID: operations[0].ScopeID, OriginInstanceID: operations[0].OriginInstanceID}
	for _, op := range operations {
		receipt, err := receiver.Receive(ctx, store, connection, []packetissuance.Operation{op})
		if err != nil {
			t.Fatal(err)
		}
		if receipt[0].Outcome != "applied" {
			t.Fatalf("receipt=%+v", receipt)
		}
		member, err := store.GetMember(ctx, op.Command.RegistrationID)
		if err != nil || member.Number == nil {
			t.Fatalf("member=%+v err=%v", member, err)
		}
		retry, err := receiver.Receive(ctx, store, connection, []packetissuance.Operation{op})
		if err != nil || !retry[0].Known || retry[0].Outcome != "applied" {
			t.Fatalf("retry=%+v err=%v", retry, err)
		}
	}
	var changes string
	if err := store.DB().QueryRow(`SELECT changes_json FROM packet_issuance_feed_actions WHERE source_operation_id=?`, operations[1].OperationID).Scan(&changes); err != nil {
		t.Fatal(err)
	}
	var feed []packetissuance.FeedChange
	if err := json.Unmarshal([]byte(changes), &feed); err != nil {
		t.Fatal(err)
	}
	if len(feed) != 1 || feed[0].Before != nil || feed[0].After.Bib != "576" {
		t.Fatalf("feed=%s", changes)
	}
	// A different walk-in from an offline tablet cannot take the accepted bib.
	var raw map[string]any
	if err := json.Unmarshal(operations[1].CanonicalJSON(), &raw); err != nil {
		t.Fatal(err)
	}
	raw["operationId"] = "33333333-3333-4333-8333-333333333333"
	raw["originSequence"] = 3
	command := raw["command"].(map[string]any)
	command["registrationId"], command["bib"] = "123456789012346", "401"
	raw["bases"].([]any)[0].(map[string]any)["registrationId"] = "123456789012346"
	change := raw["changes"].([]any)[0].(map[string]any)
	change["registrationId"] = "123456789012346"
	change["before"].(map[string]any)["id"] = "123456789012346"
	change["after"].(map[string]any)["id"] = "123456789012346"
	change["after"].(map[string]any)["bib"] = "401"
	encoded, _ := json.Marshal(raw)
	conflict, err := packetissuance.ParseOperation(encoded)
	if err != nil {
		t.Fatal(err)
	}
	receipts, err := receiver.Receive(ctx, store, connection, []packetissuance.Operation{conflict})
	if err != nil || receipts[0].Outcome != "conflict" || *receipts[0].Code != "number_occupied" {
		t.Fatalf("receipts=%+v err=%v", receipts, err)
	}
}

func TestPacketNumbersFeedToSecondDeskAndNativeEdit(t *testing.T) {
	operations := numberOperations(t)
	producer, consumer := packetStore(t, operations[0]), packetStore(t, operations[0])
	ctx := context.Background()
	connection := PacketConnectionContext{ConnectionID: "tablet-a", EventID: "621632", ScopeID: operations[0].ScopeID, OriginInstanceID: operations[0].OriginInstanceID}
	receipts, err := NewPacketIssuanceReceiver().Receive(ctx, producer, connection, operations)
	if err != nil || receipts[0].Outcome != "applied" || receipts[1].Outcome != "applied" {
		t.Fatalf("receipts=%+v err=%v", receipts, err)
	}
	page, err := PacketIssuanceFeedPage(ctx, producer, "621632", operations[0].ScopeID, "0", 100)
	if err != nil {
		t.Fatal(err)
	}
	applications, err := ApplyPacketFeedPage(ctx, consumer, "621632", page)
	if err != nil || len(applications) != 2 || applications[0].Application != "applied" || applications[1].Application != "applied" {
		t.Fatalf("applications=%+v err=%v", applications, err)
	}
	member, err := consumer.GetMember(ctx, "123456789012345")
	if err != nil || member.Number == nil || *member.Number != 576 {
		t.Fatalf("member=%+v err=%v", member, err)
	}
	if _, err := ApplyEdit(ctx, consumer, EditRequest{Entity: "member", EntityID: member.ID, Field: "number", Value: json.RawMessage(`577`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyEdit(ctx, consumer, EditRequest{Entity: "member", EntityID: member.ID, Field: "city", Value: json.RawMessage(`"Саратов"`)}); err != nil {
		t.Fatal(err)
	}
	local, err := consumer.ListLocalChanges(ctx)
	if err != nil || len(local) != 2 || local[0].EntityID != member.ID {
		t.Fatalf("site outbox=%+v err=%v", local, err)
	}
	feed, err := PacketIssuanceFeedPage(ctx, consumer, "621632", operations[0].ScopeID, "0", 100)
	if err != nil || len(feed.Actions) != 4 {
		t.Fatalf("feed=%+v err=%v", feed, err)
	}
	payload, summary, err := BuildSyncPayloadV3(ctx, consumer, "621632", true, nil)
	if err != nil || summary.MemberEdits != 1 {
		t.Fatalf("sync summary=%+v err=%v", summary, err)
	}
	var decoded syncPayload
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.MemberEdits[0].MemberRef.MemberID == nil || *decoded.MemberEdits[0].MemberRef.MemberID != member.ID {
		t.Fatalf("new issuance participant lost stable site identity: %s", payload)
	}
	if path := os.Getenv("CHR_SW021_SYNC_ARTIFACT"); path != "" {
		if err := os.WriteFile(path, payload, 0600); err != nil {
			t.Fatal(err)
		}
	}
	last := feed.Actions[3]
	if last.SourceCode != "chrono_desk.local_edit" || last.Changes[0].After.Bib != "577" || last.Changes[0].After.Person.City != "Саратов" {
		t.Fatalf("last=%+v", last)
	}
}

func TestDiscardConflictingWalkInPublishesAbsentRegistration(t *testing.T) {
	ctx := context.Background()
	operations := numberOperations(t)
	dir := t.TempDir()
	db, err := sqlite.Open(filepath.Join(dir, "621632.chrono"))
	if err != nil {
		t.Fatal(err)
	}
	seed, err := sqlite.New(db)
	if err != nil {
		t.Fatal(err)
	}
	seedOperation := operations[0]
	seedOperation.Changes[0].Before.Bib = "17"
	seedPacketResolutionEvent(t, seed, seedOperation)
	// The imported normal member occupies the walk-in's requested number.
	if _, err := seed.DB().Exec(`UPDATE members SET number=576`); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	events, err := NewEventManager(dir, log.Default())
	if err != nil {
		t.Fatal(err)
	}
	store, err := events.Open("621632")
	if err != nil {
		t.Fatal(err)
	}
	connection := PacketConnectionContext{ConnectionID: "tablet-a", EventID: "621632", ScopeID: operations[1].ScopeID, OriginInstanceID: operations[1].OriginInstanceID}
	receipts, err := NewPacketIssuanceReceiver().Receive(ctx, store, connection, operations[1:])
	if err != nil || receipts[0].Outcome != "conflict" || *receipts[0].Code != "number_occupied" {
		t.Fatalf("receipts=%+v err=%v", receipts, err)
	}
	page, err := ListPacketConflictCandidates(ctx, store, "621632", true, 50, 0)
	if err != nil || len(page.Items) != 1 || page.Items[0].ProposalApplicable {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	resolution, err := ResolvePacketConflict(ctx, events, "621632", operations[1].OperationID, false,
		"Выбрать другой номер", "Судья", page.Items[0].Evidence)
	if err != nil {
		t.Fatal(err)
	}
	feed, err := PacketIssuanceFeedPage(ctx, store, "621632", operations[1].ScopeID, "1", 100)
	if err != nil || len(feed.Actions) != 1 || feed.Actions[0].Operation.OperationID != resolution.OperationID || feed.Actions[0].Changes[0].After != nil {
		t.Fatalf("feed=%+v err=%v", feed, err)
	}
	if _, err := store.GetMember(ctx, operations[1].Command.RegistrationID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("discarded creation persisted: %v", err)
	}
}
