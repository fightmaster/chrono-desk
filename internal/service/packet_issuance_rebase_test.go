package service

import (
	"context"
	"strings"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/packetissuance"
)

func packetRebaseSnapshot(operation packetissuance.Operation, row packetissuance.Registration) packetissuance.Bootstrap {
	return packetissuance.Bootstrap{
		SchemaVersion: 2,
		ScopeID:       operation.ScopeID,
		SourceKind:    "site",
		Event:         packetissuance.Event{ID: row.EventID, Name: "Тест", Date: "2026-09-13"},
		Races:         []packetissuance.Race{{ID: row.RaceID, Name: "5 км"}},
		Registrations: []packetissuance.Registration{row},
		BaselineID:    "snapshot:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		FeedCursor:    "42",
	}
}

func TestPacketSnapshotRebaseMergesSiteChangeAndRetainsPendingLocalOperation(t *testing.T) {
	operation := packetFixtureOperation(t)
	store := packetStore(t, operation)
	ctx := context.Background()
	connection := PacketConnectionContext{EventID: "621632", ScopeID: operation.ScopeID, OriginInstanceID: operation.OriginInstanceID}
	if _, err := NewPacketIssuanceReceiver().Receive(ctx, store, connection, []packetissuance.Operation{operation}); err != nil {
		t.Fatal(err)
	}
	server := operation.Changes[0].Before
	server.Person.City = "Энгельс"
	result, err := RebasePacketIssuanceRoster(ctx, store, "621632", packetRebaseSnapshot(operation, server))
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.FindPacketRegistration(ctx, "621632", "700")
	if err != nil || !current.Value.Issued || current.Value.Person.City != "Энгельс" ||
		len(current.Heads) != 1 || current.Heads[0] != operation.OperationID {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	pending, err := store.ListPendingPacketOperations(ctx, "621632", 64)
	if err != nil || len(pending) != 1 || pending[0].OperationID != operation.OperationID {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	scope, _ := store.GetPacketIssuanceScope(ctx, "621632")
	baseline, _ := store.FindPacketSiteRegistration(ctx, "621632", "700")
	if result.Applied != 1 || result.Review != 0 || scope.SiteFeedCursor != "42" ||
		scope.BaselineID != "snapshot:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" ||
		baseline.Value.Person.City != "Энгельс" {
		t.Fatalf("result=%+v scope=%+v baseline=%+v", result, scope, baseline)
	}
	var rebases int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM packet_issuance_snapshot_rebases`).Scan(&rebases); err != nil || rebases != 1 {
		t.Fatalf("rebases=%d err=%v", rebases, err)
	}
}

func TestPacketSnapshotRebaseKeepsSameFieldConflictForReview(t *testing.T) {
	operation := packetFixtureOperation(t)
	store := packetStore(t, operation)
	ctx := context.Background()
	local := operation.Changes[0].Before
	local.Person.City = "Локально"
	if err := store.PutPacketRegistration(ctx, local, []string{"local-head"}, nil); err != nil {
		t.Fatal(err)
	}
	server := operation.Changes[0].Before
	server.Person.City = "На сайте"
	result, err := RebasePacketIssuanceRoster(ctx, store, "621632", packetRebaseSnapshot(operation, server))
	if err != nil {
		t.Fatal(err)
	}
	current, _ := store.FindPacketRegistration(ctx, "621632", "700")
	baseline, _ := store.FindPacketSiteRegistration(ctx, "621632", "700")
	if result.Review != 1 || result.Items[0].Code == nil || *result.Items[0].Code != "feed_state_conflict" ||
		current.Value.Person.City != "Локально" || baseline.Value.Person.City != "На сайте" ||
		len(current.Heads) != 1 || current.Heads[0] != "local-head" {
		t.Fatalf("result=%+v current=%+v baseline=%+v", result, current, baseline)
	}
}

func TestPacketSnapshotRebaseNeverTreatsSnapshotAbsenceAsDeletion(t *testing.T) {
	operation := packetFixtureOperation(t)
	store := packetStore(t, operation)
	snapshot := packetRebaseSnapshot(operation, operation.Changes[0].Before)
	snapshot.Registrations = nil
	result, err := RebasePacketIssuanceRoster(context.Background(), store, "621632", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	current, _ := store.FindPacketRegistration(context.Background(), "621632", "700")
	if !current.Found || current.Deleted || result.Review != 1 || result.Items[0].Code == nil ||
		*result.Items[0].Code != "snapshot_registration_missing" {
		t.Fatalf("result=%+v current=%+v", result, current)
	}
}

func TestPacketSnapshotRebaseRollsBackEverythingWhenAuditWriteFails(t *testing.T) {
	operation := packetFixtureOperation(t)
	store := packetStore(t, operation)
	server := operation.Changes[0].Before
	server.Person.City = "Энгельс"
	if _, err := store.DB().Exec(`CREATE TRIGGER fail_packet_rebase BEFORE INSERT ON packet_issuance_snapshot_rebases BEGIN SELECT RAISE(FAIL,'forced'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := RebasePacketIssuanceRoster(context.Background(), store, "621632", packetRebaseSnapshot(operation, server)); err == nil {
		t.Fatal("expected audit failure")
	}
	current, _ := store.FindPacketRegistration(context.Background(), "621632", "700")
	baseline, _ := store.FindPacketSiteRegistration(context.Background(), "621632", "700")
	scope, _ := store.GetPacketIssuanceScope(context.Background(), "621632")
	if current.Value.Person.City != "Саратов" || baseline.Value.Person.City != "Саратов" || scope.SiteFeedCursor != "0" {
		t.Fatalf("current=%+v baseline=%+v scope=%+v", current, baseline, scope)
	}
}

func TestPacketSnapshotRebaseFailsClosedWithoutSeparateSiteBaseline(t *testing.T) {
	operation := packetFixtureOperation(t)
	store := packetStore(t, operation)
	if _, err := store.DB().Exec(`DELETE FROM packet_issuance_site_registrations WHERE event_id='621632'`); err != nil {
		t.Fatal(err)
	}
	_, err := RebasePacketIssuanceRoster(context.Background(), store, "621632",
		packetRebaseSnapshot(operation, operation.Changes[0].Before))
	if err == nil || !strings.Contains(err.Error(), "packet_site_baseline_unavailable") {
		t.Fatalf("error=%v", err)
	}
}
