package service

import (
	"context"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/packetissuance"
)

func TestPacketFeedRetentionArchivesContiguousPrefixAndExpiresOldCursor(t *testing.T) {
	operation := packetFixtureOperation(t)
	store := packetStore(t, operation)
	ctx := context.Background()
	connection := PacketConnectionContext{EventID: "621632", ScopeID: operation.ScopeID, OriginInstanceID: operation.OriginInstanceID}
	if _, err := NewPacketIssuanceReceiver().Receive(ctx, store, connection, []packetissuance.Operation{operation}); err != nil {
		t.Fatal(err)
	}
	firstServerAction := "22222222-2222-4222-8222-222222222222"
	for index, id := range []string{firstServerAction, "33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444"} {
		if err := store.PublishPacketServerChange(ctx, "621632", id, "chrono_desk.local_edit",
			operation.Changes, time.Now().UnixMilli()+int64(index)); err != nil {
			t.Fatal(err)
		}
	}
	dryRun, err := CompactPacketIssuanceFeed(ctx, store, "621632", 2, false, time.Now())
	if err != nil || dryRun.Candidate != 2 || dryRun.Processed != 2 || dryRun.Executed {
		t.Fatalf("dry-run=%+v err=%v", dryRun, err)
	}
	result, err := CompactPacketIssuanceFeed(ctx, store, "621632", 2, true, time.Now())
	if err != nil || !result.Executed || result.FirstAvailable != 3 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var live, archived int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM packet_issuance_feed_actions`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM packet_issuance_feed_archives`).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if live != 2 || archived != 2 {
		t.Fatalf("live=%d archived=%d", live, archived)
	}
	if _, err := PacketIssuanceFeedPage(ctx, store, "621632", operation.ScopeID, "0", 100); err != ErrPacketFeedCursorExpired {
		t.Fatalf("old cursor error=%v", err)
	}
	page, err := PacketIssuanceFeedPage(ctx, store, "621632", operation.ScopeID, "2", 100)
	if err != nil || len(page.Actions) != 2 || page.Cursor.Next != "4" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	applied, err := store.PacketDependenciesApplied(ctx, "621632", []string{firstServerAction})
	if err != nil || !applied {
		t.Fatalf("archived dependency applied=%t err=%v", applied, err)
	}
	retry, err := NewPacketIssuanceReceiver().Receive(ctx, store, connection, []packetissuance.Operation{operation})
	bounds, boundsErr := store.PacketFeedBounds(ctx, "621632")
	if err != nil || boundsErr != nil || !retry[0].Known || bounds.Head != 4 {
		t.Fatalf("retry=%+v err=%v bounds=%+v boundsErr=%v", retry, err, bounds, boundsErr)
	}
}

func TestPacketFeedRetentionRollsBackArchiveDeleteAndFloor(t *testing.T) {
	operation := packetFixtureOperation(t)
	store := packetStore(t, operation)
	ctx := context.Background()
	connection := PacketConnectionContext{EventID: "621632", ScopeID: operation.ScopeID, OriginInstanceID: operation.OriginInstanceID}
	if _, err := NewPacketIssuanceReceiver().Receive(ctx, store, connection, []packetissuance.Operation{operation}); err != nil {
		t.Fatal(err)
	}
	if err := store.PublishPacketServerChange(ctx, "621632", "22222222-2222-4222-8222-222222222222",
		"chrono_desk.local_edit", operation.Changes, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`CREATE TRIGGER fail_packet_archive BEFORE INSERT ON packet_issuance_feed_archives BEGIN SELECT RAISE(FAIL,'forced'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := CompactPacketIssuanceFeed(ctx, store, "621632", 1, true, time.Now()); err == nil {
		t.Fatal("expected archive failure")
	}
	bounds, err := store.PacketFeedBounds(ctx, "621632")
	if err != nil || bounds.Head != 2 || bounds.FirstAvailable != 1 {
		t.Fatalf("bounds=%+v err=%v", bounds, err)
	}
	var live, archived int
	_ = store.DB().QueryRow(`SELECT COUNT(*) FROM packet_issuance_feed_actions`).Scan(&live)
	_ = store.DB().QueryRow(`SELECT COUNT(*) FROM packet_issuance_feed_archives`).Scan(&archived)
	if live != 2 || archived != 0 {
		t.Fatalf("live=%d archived=%d", live, archived)
	}
}
