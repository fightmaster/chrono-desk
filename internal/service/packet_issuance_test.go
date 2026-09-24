package service

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/packetissuance"
)

func packetFixtureOperation(t *testing.T) packetissuance.Operation {
	t.Helper()
	data, err := os.ReadFile("../packetissuance/testdata/packet-issuance-operations-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Operation json.RawMessage `json:"operation"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	operation, err := packetissuance.ParseOperation(fixture.Operation)
	if err != nil {
		t.Fatal(err)
	}
	return operation
}

func packetStore(t *testing.T, operation packetissuance.Operation) *sqlite.Store {
	t.Helper()
	store := newTestStore(t)
	ctx := context.Background()
	row := operation.Changes[0].Before
	if err := store.UpsertEvent(ctx, domain.Event{ID: row.EventID, Name: "Тест", Date: "2026-09-13"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRace(ctx, domain.Race{ID: row.RaceID, EventID: row.EventID, Name: "5 км", Date: "2026-09-13 09:00:00", Format: domain.FormatFixedDistance}); err != nil {
		t.Fatal(err)
	}
	var number *int64
	if row.Bib != "" {
		value, err := strconv.ParseInt(row.Bib, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		number = &value
	}
	epc := row.EPC
	gender := row.Person.Gender
	dob := row.Person.BirthDate
	team := row.Person.Team
	city := row.Person.City
	if err := store.UpsertMember(ctx, domain.Member{ID: row.ID, EventID: row.EventID, RaceID: row.RaceID, Number: number, EPC: &epc, FirstName: row.Person.FirstName, LastName: row.Person.LastName, Gender: &gender, DOB: &dob, Team: &team, City: &city}); err != nil {
		t.Fatal(err)
	}
	if err := store.InstallPacketIssuanceRoster(ctx, sqlite.PacketIssuanceScope{EventID: row.EventID, ScopeID: operation.ScopeID, BaselineID: operation.BaselineID, SourceKind: "site"}, []packetissuance.Registration{row}); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestPacketIssuanceReceiverCommitsProjectionJournalAndFeedAtomically(t *testing.T) {
	operation := packetFixtureOperation(t)
	store := packetStore(t, operation)
	receiver := NewPacketIssuanceReceiver()
	ctx := context.Background()
	connection := PacketConnectionContext{ConnectionID: "desk-connection", EventID: "621632", ScopeID: operation.ScopeID, OriginInstanceID: operation.OriginInstanceID}
	receipts, err := receiver.Receive(ctx, store, connection, []packetissuance.Operation{operation})
	if err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 1 || receipts[0].Outcome != "applied" || receipts[0].Known {
		t.Fatalf("receipt=%+v", receipts)
	}
	rows, err := store.ListPacketRegistrations(ctx, "621632")
	if err != nil {
		t.Fatal(err)
	}
	if !rows[0].Issued || rows[0].Bib != "0017" {
		t.Fatalf("projection=%+v", rows[0])
	}
	for table, want := range map[string]int{"packet_issuance_operations": 1, "packet_issuance_feed_actions": 1} {
		var got int
		if err := store.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s=%d", table, got)
		}
	}
	retry, err := receiver.Receive(ctx, store, connection, []packetissuance.Operation{operation})
	if err != nil {
		t.Fatal(err)
	}
	if !retry[0].Known || retry[0].Outcome != "applied" {
		t.Fatalf("retry=%+v", retry)
	}
	var feeds int
	_ = store.DB().QueryRow(`SELECT COUNT(*) FROM packet_issuance_feed_actions`).Scan(&feeds)
	if feeds != 1 {
		t.Fatalf("retry duplicated feed: %d", feeds)
	}
}

func TestPacketIssuanceReceiverRollsBackWhenFeedCommitFails(t *testing.T) {
	operation := packetFixtureOperation(t)
	store := packetStore(t, operation)
	ctx := context.Background()
	if _, err := store.DB().Exec(`CREATE TRIGGER fail_packet_feed BEFORE INSERT ON packet_issuance_feed_actions BEGIN SELECT RAISE(FAIL,'forced feed failure'); END`); err != nil {
		t.Fatal(err)
	}
	connection := PacketConnectionContext{EventID: "621632", ScopeID: operation.ScopeID, OriginInstanceID: operation.OriginInstanceID}
	if _, err := NewPacketIssuanceReceiver().Receive(ctx, store, connection, []packetissuance.Operation{operation}); err == nil {
		t.Fatal("expected feed failure")
	}
	rows, err := store.ListPacketRegistrations(ctx, "621632")
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Issued {
		t.Fatal("issuance survived failed feed commit")
	}
	var operations int
	_ = store.DB().QueryRow(`SELECT COUNT(*) FROM packet_issuance_operations`).Scan(&operations)
	if operations != 0 {
		t.Fatal("operation survived failed feed commit")
	}
}

func TestPacketIssuanceRosterRetainsStringBibAndRejectsPartialSnapshot(t *testing.T) {
	operation := packetFixtureOperation(t)
	store := packetStore(t, operation)
	rows, err := store.ListPacketRegistrations(context.Background(), "621632")
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Bib != "0017" {
		t.Fatalf("bib=%q", rows[0].Bib)
	}
	if err := store.InstallPacketIssuanceRoster(context.Background(), sqlite.PacketIssuanceScope{EventID: "621632", ScopeID: operation.ScopeID, BaselineID: operation.BaselineID, SourceKind: "site"}, nil); err == nil {
		t.Fatal("partial snapshot accepted")
	}
}

func TestPacketIssuanceRosterRejectsUnreconciledDeskProfileEdit(t *testing.T) {
	operation := packetFixtureOperation(t)
	store := packetStore(t, operation)
	row := operation.Changes[0].Before
	if _, err := store.DB().Exec(`UPDATE members SET first_name='Несинхронизированный' WHERE id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.InstallPacketIssuanceRoster(context.Background(), sqlite.PacketIssuanceScope{
		EventID: row.EventID, ScopeID: operation.ScopeID, BaselineID: operation.BaselineID, SourceKind: "site",
	}, []packetissuance.Registration{row}); err == nil {
		t.Fatal("site roster replaced an unreconciled Desk profile edit")
	}
}

func TestPacketIssuanceSiteOutboxKeepsOperationUntilMatchingTerminalReceipt(t *testing.T) {
	operation := packetFixtureOperation(t)
	store := packetStore(t, operation)
	row := operation.Changes[0].Before
	ctx := context.Background()
	connection := PacketConnectionContext{EventID: row.EventID, ScopeID: operation.ScopeID, OriginInstanceID: operation.OriginInstanceID}
	receipts, err := NewPacketIssuanceReceiver().Receive(ctx, store, connection, []packetissuance.Operation{operation})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.ListPendingPacketOperations(ctx, row.EventID, 64)
	if err != nil || len(pending) != 1 || pending[0].OperationID != operation.OperationID {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	if err := store.MarkPacketOperationSiteReceipt(ctx, receipts[0]); err != nil {
		t.Fatal(err)
	}
	pending, err = store.ListPendingPacketOperations(ctx, row.EventID, 64)
	if err != nil || len(pending) != 0 {
		t.Fatalf("acknowledged operation remains pending: %+v err=%v", pending, err)
	}
	bad := receipts[0]
	bad.ContentHash = "sha256:wrong"
	if err := store.MarkPacketOperationSiteReceipt(ctx, bad); err == nil {
		t.Fatal("mismatched receipt updated operation")
	}
}
