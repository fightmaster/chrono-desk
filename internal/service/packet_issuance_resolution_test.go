package service

import (
	"context"
	"log"
	"path/filepath"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/packetissuance"
)

func TestChronoDeskResolutionIsOnePendingImmutableOperationAndLANFeedAction(t *testing.T) {
	ctx := context.Background()
	operation := packetFixtureOperation(t)
	dir := t.TempDir()
	db, err := sqlite.Open(filepath.Join(dir, "621632.chrono"))
	if err != nil {
		t.Fatal(err)
	}
	seed, err := sqlite.New(db)
	if err != nil {
		t.Fatal(err)
	}
	seedPacketResolutionEvent(t, seed, operation)
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
	code := "current_state_changed"
	if err := store.SavePacketOperation(ctx, operation, packetissuance.ContentHash(operation), "conflict", &code, time.Now().UnixMilli(), false); err != nil {
		t.Fatal(err)
	}
	if err := store.PublishPacketOperation(ctx, operation, "conflict", &code, nil, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}

	page, err := ListPacketConflictCandidates(ctx, store, "621632", true, 50, 0)
	if err != nil || page.Total != 1 || len(page.Items) != 1 || !page.Items[0].ProposalApplicable {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	resolution, err := ResolvePacketConflict(ctx, events, "621632", operation.OperationID, true,
		"Проверено по физическому пакету", "Старший судья", page.Items[0].Evidence)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.SchemaVersion != 2 || resolution.OriginInstanceID != events.InstallationID() ||
		len(resolution.Command.Keep) != 1 || resolution.Command.Keep[0] != operation.OperationID {
		t.Fatalf("resolution=%+v", resolution)
	}
	rows, err := store.ListPacketRegistrations(ctx, "621632")
	if err != nil || len(rows) != 1 || !rows[0].Issued {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	registration, err := store.FindPacketRegistration(ctx, "621632", rows[0].ID)
	if err != nil || len(registration.Heads) != 1 || registration.Heads[0] != resolution.OperationID {
		t.Fatalf("registration=%+v err=%v", registration, err)
	}
	pending, err := store.ListPendingPacketOperations(ctx, "621632", 64)
	foundResolution := false
	for _, item := range pending {
		foundResolution = foundResolution || item.OperationID == resolution.OperationID && item.SchemaVersion == 2
	}
	if err != nil || len(pending) != 2 || !foundResolution {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	unresolved, err := ListPacketConflictCandidates(ctx, store, "621632", true, 50, 0)
	if err != nil || unresolved.Total != 0 {
		t.Fatalf("unresolved=%+v err=%v", unresolved, err)
	}
	feed, err := PacketIssuanceFeedPage(ctx, store, "621632", operation.ScopeID, "1", 100)
	if err != nil || len(feed.Actions) != 1 || feed.Actions[0].Operation == nil ||
		feed.Actions[0].Operation.OperationID != resolution.OperationID {
		t.Fatalf("feed=%+v err=%v", feed, err)
	}
}

func seedPacketResolutionEvent(t *testing.T, store *sqlite.Store, operation packetissuance.Operation) {
	t.Helper()
	ctx := context.Background()
	row := operation.Changes[0].Before
	if err := store.UpsertEvent(ctx, domain.Event{ID: row.EventID, Name: "Тест", Date: "2026-09-13"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRace(ctx, domain.Race{ID: row.RaceID, EventID: row.EventID, Name: "5 км", Date: "2026-09-13 09:00:00", Format: domain.FormatFixedDistance}); err != nil {
		t.Fatal(err)
	}
	number := int64(17)
	epc := row.EPC
	gender := row.Person.Gender
	dob := row.Person.BirthDate
	team := row.Person.Team
	city := row.Person.City
	if err := store.UpsertMember(ctx, domain.Member{ID: row.ID, EventID: row.EventID, RaceID: row.RaceID,
		Number: &number, EPC: &epc, FirstName: row.Person.FirstName, LastName: row.Person.LastName,
		Gender: &gender, DOB: &dob, Team: &team, City: &city}); err != nil {
		t.Fatal(err)
	}
	if err := store.InstallPacketIssuanceRoster(ctx, sqlite.PacketIssuanceScope{EventID: row.EventID,
		ScopeID: operation.ScopeID, BaselineID: operation.BaselineID, SourceKind: "site"},
		[]packetissuance.Registration{row}); err != nil {
		t.Fatal(err)
	}
}
