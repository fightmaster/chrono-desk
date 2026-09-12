package service

import (
	"context"
	"os"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/packetissuance"
)

func packetFeedFixture(t *testing.T) packetissuance.FeedPage {
	t.Helper()
	raw, err := os.ReadFile("../packetissuance/testdata/packet-issuance-feed-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	page, err := packetissuance.ParseFeedPage(raw,
		"site:22222222-2222-4222-8222-222222222222:621632", "16")
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func preparedPacketFeedStore(t *testing.T) (*sqlite.Store, packetissuance.Operation) {
	t.Helper()
	operation := packetFixtureOperation(t)
	store := packetStore(t, operation)
	if _, err := store.DB().Exec(`UPDATE packet_issuance_scopes SET site_feed_cursor='16'`); err != nil {
		t.Fatal(err)
	}
	return store, operation
}

func TestApplyPacketFeedPageCommitsProjectionActionsAndCursorAtomically(t *testing.T) {
	store, _ := preparedPacketFeedStore(t)
	ctx := context.Background()
	if _, err := store.DB().Exec(`UPDATE members SET finish_time_ms=1789291000000 WHERE id='700'`); err != nil {
		t.Fatal(err)
	}
	applications, err := ApplyPacketFeedPage(ctx, store, "621632", packetFeedFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListPacketRegistrations(ctx, "621632")
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if !rows[0].Issued || rows[0].Person.FirstName != "Анна-Мария" || !rows[0].HasTimingEvidence {
		t.Fatalf("projection=%+v", rows[0])
	}
	scope, err := store.GetPacketIssuanceScope(ctx, "621632")
	if err != nil || scope.SiteFeedCursor != "18" || len(applications) != 2 {
		t.Fatalf("scope=%+v applications=%+v err=%v", scope, applications, err)
	}
	var actions int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM packet_issuance_site_feed_actions`).Scan(&actions); err != nil || actions != 2 {
		t.Fatalf("actions=%d err=%v", actions, err)
	}
}

func TestApplyPacketFeedPageKeepsIncompatibleLocalEditForReview(t *testing.T) {
	store, operation := preparedPacketFeedStore(t)
	ctx := context.Background()
	row := operation.Changes[0].Before
	row.Person.FirstName = "Локальная правка"
	if err := store.PutPacketRegistration(ctx, row, nil, nil); err != nil {
		t.Fatal(err)
	}
	applications, err := ApplyPacketFeedPage(ctx, store, "621632", packetFeedFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := store.ListPacketRegistrations(ctx, "621632")
	if rows[0].Person.FirstName != "Локальная правка" || !rows[0].Issued {
		t.Fatalf("local edit or compatible issuance lost: %+v", rows[0])
	}
	if applications[1].Application != "review" || applications[1].Code == nil || *applications[1].Code != "feed_state_conflict" {
		t.Fatalf("applications=%+v", applications)
	}
}

func TestApplyPacketFeedPageRollsBackProjectionAndCursorOnJournalFailure(t *testing.T) {
	store, _ := preparedPacketFeedStore(t)
	ctx := context.Background()
	if _, err := store.DB().Exec(`CREATE TRIGGER fail_site_packet_feed BEFORE INSERT ON packet_issuance_site_feed_actions BEGIN SELECT RAISE(FAIL,'forced'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPacketFeedPage(ctx, store, "621632", packetFeedFixture(t)); err == nil {
		t.Fatal("expected journal failure")
	}
	rows, _ := store.ListPacketRegistrations(ctx, "621632")
	scope, _ := store.GetPacketIssuanceScope(ctx, "621632")
	if rows[0].Issued || scope.SiteFeedCursor != "16" {
		t.Fatalf("projection=%+v cursor=%s", rows[0], scope.SiteFeedCursor)
	}
}
