package service

import (
	"context"
	"encoding/json"
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

func packetResolutionFeedFixture(t *testing.T) packetissuance.FeedPage {
	t.Helper()
	inputData, err := os.ReadFile("../packetissuance/testdata/packet-issuance-operations-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	resolutionData, err := os.ReadFile("../packetissuance/testdata/packet-issuance-resolution-operation-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var inputFixture struct {
		Operation map[string]any `json:"operation"`
	}
	var resolutionFixture struct {
		ResolutionOperation map[string]any `json:"resolutionOperation"`
	}
	if json.Unmarshal(inputData, &inputFixture) != nil || json.Unmarshal(resolutionData, &resolutionFixture) != nil {
		t.Fatal("decode packet resolution fixtures")
	}
	feedChanges := func(operation map[string]any) []any {
		raw := operation["changes"].([]any)
		changes := make([]any, 0, len(raw))
		for _, item := range raw {
			encoded, _ := json.Marshal(item)
			var change map[string]any
			_ = json.Unmarshal(encoded, &change)
			delete(change["before"].(map[string]any), "hasTimingEvidence")
			delete(change["after"].(map[string]any), "hasTimingEvidence")
			changes = append(changes, change)
		}
		return changes
	}
	scopeID := inputFixture.Operation["scopeId"].(string)
	page := map[string]any{
		"schemaVersion": 1, "scopeId": scopeID,
		"cursor": map[string]any{"after": "16", "next": "18", "head": "18", "hasMore": false},
		"actions": []any{
			map[string]any{"actionId": inputFixture.Operation["operationId"], "kind": "operation", "sequence": "17",
				"recordedAt": "2026-09-13T08:00:00.123Z", "sourceCode": "pwa.packet_issuance",
				"outcome": "conflict", "code": "current_state_changed", "operation": inputFixture.Operation, "changes": []any{}},
			map[string]any{"actionId": resolutionFixture.ResolutionOperation["operationId"], "kind": "operation", "sequence": "18",
				"recordedAt": "2026-09-13T08:01:00.123Z", "sourceCode": "admin.packet_issuance",
				"outcome": "applied", "code": nil, "operation": resolutionFixture.ResolutionOperation,
				"changes": feedChanges(resolutionFixture.ResolutionOperation)},
		},
	}
	wire, _ := json.Marshal(page)
	parsed, err := packetissuance.ParseFeedPage(wire, scopeID, "16")
	if err != nil {
		t.Fatalf("parse resolution feed: %v", err)
	}
	return parsed
}

func competingPacketResolutionFeedFixture(t *testing.T) packetissuance.FeedPage {
	t.Helper()
	data, err := os.ReadFile("../packetissuance/testdata/packet-issuance-resolution-operation-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		ResolutionOperation map[string]any `json:"resolutionOperation"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	operation := fixture.ResolutionOperation
	operation["operationId"] = "99999999-9999-4999-8999-999999999999"
	operation["originSequence"] = float64(2)
	rawChanges := operation["changes"].([]any)
	changes := make([]any, 0, len(rawChanges))
	for _, item := range rawChanges {
		encoded, _ := json.Marshal(item)
		var change map[string]any
		_ = json.Unmarshal(encoded, &change)
		delete(change["before"].(map[string]any), "hasTimingEvidence")
		delete(change["after"].(map[string]any), "hasTimingEvidence")
		changes = append(changes, change)
	}
	page := map[string]any{
		"schemaVersion": 1, "scopeId": operation["scopeId"],
		"cursor": map[string]any{"after": "18", "next": "19", "head": "19", "hasMore": false},
		"actions": []any{map[string]any{
			"actionId": operation["operationId"], "kind": "operation", "sequence": "19",
			"recordedAt": "2026-09-13T08:02:00.123Z", "sourceCode": "admin.packet_issuance",
			"outcome": "applied", "code": nil, "operation": operation, "changes": changes,
		}},
	}
	wire, _ := json.Marshal(page)
	parsed, err := packetissuance.ParseFeedPage(wire, operation["scopeId"].(string), "18")
	if err != nil {
		t.Fatalf("parse competing resolution feed: %v", err)
	}
	return parsed
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

func TestApplyPacketResolutionConvergesRejectedBranchAndRelaysToLAN(t *testing.T) {
	store, operation := preparedPacketFeedStore(t)
	ctx := context.Background()
	local := operation.Changes[0].Before
	local.Person.FirstName = "Локальная Анна"
	if err := store.PutPacketRegistration(ctx, local, nil, nil); err != nil {
		t.Fatal(err)
	}
	applications, err := ApplyPacketFeedPage(ctx, store, "621632", packetResolutionFeedFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := store.ListPacketRegistrations(ctx, "621632")
	if rows[0].Issued || rows[0].Person.FirstName != "Локальная Анна" || len(applications) != 2 ||
		applications[0].Application != "review" || applications[1].Application != "applied" {
		t.Fatalf("rows=%+v applications=%+v", rows, applications)
	}
	record, err := store.FindPacketRegistration(ctx, "621632", "700")
	if err != nil || len(record.Heads) != 1 || record.Heads[0] != "77777777-7777-4777-8777-777777777777" {
		t.Fatalf("heads=%v err=%v", record.Heads, err)
	}
	var resolutions int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM packet_issuance_resolutions`).Scan(&resolutions); err != nil || resolutions != 1 {
		t.Fatalf("resolutions=%d err=%v", resolutions, err)
	}
	input, found, err := store.FindPacketOperation(ctx, "11111111-1111-4111-8111-111111111111")
	if err != nil || !found || input.Outcome != "conflict" {
		t.Fatalf("input outcome=%+v found=%v err=%v", input, found, err)
	}
	lan, err := PacketIssuanceFeedPage(ctx, store, "621632",
		"site:22222222-2222-4222-8222-222222222222:621632", "0", 100)
	if err != nil || len(lan.Actions) != 2 || lan.Actions[1].Operation == nil || lan.Actions[1].Operation.SchemaVersion != 2 {
		t.Fatalf("lan=%+v err=%v", lan, err)
	}
	pending, err := store.ListPendingPacketOperations(ctx, "621632", 64)
	if err != nil || len(pending) != 0 {
		t.Fatalf("received operations entered site outbox: %d err=%v", len(pending), err)
	}
}

func TestApplyPacketResolutionConvergesPreviouslyAppliedBranch(t *testing.T) {
	store, operation := preparedPacketFeedStore(t)
	ctx := context.Background()
	connection := PacketConnectionContext{EventID: "621632", ScopeID: operation.ScopeID, OriginInstanceID: operation.OriginInstanceID}
	if _, err := NewPacketIssuanceReceiver().Receive(ctx, store, connection, []packetissuance.Operation{operation}); err != nil {
		t.Fatal(err)
	}
	applications, err := ApplyPacketFeedPage(ctx, store, "621632", packetResolutionFeedFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := store.ListPacketRegistrations(ctx, "621632")
	if rows[0].Issued || applications[1].Application != "applied" {
		t.Fatalf("rows=%+v applications=%+v", rows, applications)
	}
	input, found, err := store.FindPacketOperation(ctx, operation.OperationID)
	if err != nil || !found || input.Outcome != "applied" {
		t.Fatalf("local input outcome was overwritten: %+v found=%v err=%v", input, found, err)
	}
	var feeds int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM packet_issuance_feed_actions`).Scan(&feeds); err != nil || feeds != 2 {
		t.Fatalf("site echo duplicated LAN action: feeds=%d err=%v", feeds, err)
	}
}

func TestApplyPacketResolutionRollsBackRelationProjectionAndCursor(t *testing.T) {
	store, _ := preparedPacketFeedStore(t)
	ctx := context.Background()
	if _, err := store.DB().Exec(`CREATE TRIGGER fail_packet_resolution BEFORE INSERT ON packet_issuance_resolutions BEGIN SELECT RAISE(FAIL,'forced'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPacketFeedPage(ctx, store, "621632", packetResolutionFeedFixture(t)); err == nil {
		t.Fatal("expected resolution journal failure")
	}
	rows, _ := store.ListPacketRegistrations(ctx, "621632")
	scope, _ := store.GetPacketIssuanceScope(ctx, "621632")
	var actions, operations int
	_ = store.DB().QueryRow(`SELECT COUNT(*) FROM packet_issuance_site_feed_actions`).Scan(&actions)
	_ = store.DB().QueryRow(`SELECT COUNT(*) FROM packet_issuance_operations`).Scan(&operations)
	if rows[0].Issued || scope.SiteFeedCursor != "16" || actions != 0 || operations != 0 {
		t.Fatalf("projection=%+v cursor=%s actions=%d operations=%d", rows[0], scope.SiteFeedCursor, actions, operations)
	}
}

func TestApplyPacketResolutionRejectsCompetingDecisionWithoutCursorAdvance(t *testing.T) {
	store, _ := preparedPacketFeedStore(t)
	ctx := context.Background()
	if _, err := ApplyPacketFeedPage(ctx, store, "621632", packetResolutionFeedFixture(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPacketFeedPage(ctx, store, "621632", competingPacketResolutionFeedFixture(t)); err == nil || err.Error() != "packet_resolution_already_decided" {
		t.Fatalf("competing resolution error=%v", err)
	}
	scope, _ := store.GetPacketIssuanceScope(ctx, "621632")
	var resolutions, operations int
	_ = store.DB().QueryRow(`SELECT COUNT(*) FROM packet_issuance_resolutions`).Scan(&resolutions)
	_ = store.DB().QueryRow(`SELECT COUNT(*) FROM packet_issuance_operations`).Scan(&operations)
	if scope.SiteFeedCursor != "18" || resolutions != 1 || operations != 2 {
		t.Fatalf("cursor=%s resolutions=%d operations=%d", scope.SiteFeedCursor, resolutions, operations)
	}
}
