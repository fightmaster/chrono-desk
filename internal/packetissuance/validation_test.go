package packetissuance

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestCanonicalOperationFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/packet-issuance-operations-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != "f7a45fa59e90c90189e9dd655d49ea1efcee0e2cb34d766d00306f594dc4d093" {
		t.Fatalf("canonical fixture checksum drifted: %s", got)
	}
	var fixture struct {
		Operation json.RawMessage `json:"operation"`
		Expected  string          `json:"expectedContentHash"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	payload := append([]byte(`{"schemaVersion":1,"operations":[`), fixture.Operation...)
	payload = append(payload, []byte(`]}`)...)
	operations, err := ParseBatch(payload)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if got := ContentHash(operations[0]); got != fixture.Expected {
		t.Fatalf("hash=%s want=%s", got, fixture.Expected)
	}
	if !operations[0].Changes[0].After.Issued {
		t.Fatal("issue effect was not retained")
	}
}

func TestCanonicalResolutionFixtureIsTrustedFeedOnly(t *testing.T) {
	data, err := os.ReadFile("testdata/packet-issuance-resolution-operation-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprintf("%x", sha256.Sum256(data)), "257e9103ca7f6caca148c35b81064a49cb1cbd03bb534747cdc1cb16c42ca54e"; got != want {
		t.Fatalf("canonical resolution fixture checksum=%s want=%s", got, want)
	}
	var fixture struct {
		ResolutionOperation json.RawMessage `json:"resolutionOperation"`
		ExpectedContentHash string          `json:"expectedContentHash"`
		InputOperationID    string          `json:"inputOperationId"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	operation, err := ParseTrustedOperation(fixture.ResolutionOperation)
	if err != nil {
		t.Fatalf("parse trusted resolution: %v", err)
	}
	if operation.SchemaVersion != 2 || operation.Command.Type != "resolve_conflict" ||
		len(operation.Command.Inputs) != 1 || operation.Command.Inputs[0] != fixture.InputOperationID ||
		ContentHash(operation) != fixture.ExpectedContentHash {
		t.Fatalf("resolution=%+v hash=%s", operation, ContentHash(operation))
	}
	if _, err := ParseOperation(fixture.ResolutionOperation); err == nil {
		t.Fatal("resolution crossed the ordinary operation boundary")
	}
	payload := append([]byte(`{"schemaVersion":1,"operations":[`), fixture.ResolutionOperation...)
	payload = append(payload, []byte(`]}`)...)
	if _, err := ParseBatch(payload); err == nil {
		t.Fatal("resolution crossed the tablet upload boundary")
	}
}

func TestCompetingResolutionIsAVisibleTerminalFeedConflict(t *testing.T) {
	data, err := os.ReadFile("testdata/packet-issuance-resolution-operation-v1.json")
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
	scopeID := operation["scopeId"].(string)
	page := map[string]any{
		"schemaVersion": 1,
		"scopeId":       scopeID,
		"cursor": map[string]any{
			"after": "18", "next": "19", "head": "19", "hasMore": false,
		},
		"actions": []any{map[string]any{
			"actionId": operation["operationId"], "kind": "operation", "sequence": "19",
			"recordedAt": "2026-09-13T08:10:00.123Z", "sourceCode": "admin.packet_issuance_resolution",
			"outcome": "conflict", "code": "resolution_already_decided", "operation": operation,
			"changes": []any{},
		}},
	}
	wire, _ := json.Marshal(page)
	parsed, err := ParseFeedPage(wire, scopeID, "18")
	if err != nil || len(parsed.Actions) != 1 || parsed.Actions[0].Outcome != "conflict" ||
		parsed.Actions[0].Operation == nil || parsed.Actions[0].Operation.SchemaVersion != 2 {
		t.Fatalf("page=%+v err=%v", parsed, err)
	}

	page["actions"].([]any)[0].(map[string]any)["outcome"] = "equivalent"
	page["actions"].([]any)[0].(map[string]any)["code"] = nil
	wire, _ = json.Marshal(page)
	if _, err := ParseFeedPage(wire, scopeID, "18"); err == nil {
		t.Fatal("equivalent resolution outcome accepted")
	}
}

func TestOperationBoundaryRejectsUnsafeShapes(t *testing.T) {
	data, err := os.ReadFile("testdata/packet-issuance-operations-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Operation map[string]any `json:"operation"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(map[string]any){
		"unknown field": func(op map[string]any) { op["secret"] = "x" },
		"float":         func(op map[string]any) { op["originSequence"] = json.Number("1.0") },
		"different effect": func(op map[string]any) {
			op["changes"].([]any)[0].(map[string]any)["after"].(map[string]any)["issued"] = false
		},
		"unsorted heads": func(op map[string]any) {
			op["bases"].([]any)[0].(map[string]any)["heads"] = []any{"22222222-2222-4222-8222-222222222222", "11111111-1111-4111-8111-111111111111"}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			copyBytes, _ := json.Marshal(fixture.Operation)
			var op map[string]any
			d := json.NewDecoder(strings.NewReader(string(copyBytes)))
			d.UseNumber()
			_ = d.Decode(&op)
			mutate(op)
			wire, _ := json.Marshal(map[string]any{"schemaVersion": 1, "operations": []any{op}})
			if _, err := ParseBatch(wire); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestMoveToReserveCommandUsesStrictReceiverShape(t *testing.T) {
	command, err := parseCommand(map[string]any{
		"type": "move_to_reserve", "registrationId": "17", "targetId": "18", "issuePacket": true,
	}, false)
	if err != nil || command.Type != "move_to_reserve" || command.TargetID != "18" || command.IssuePacket == nil || !*command.IssuePacket {
		t.Fatalf("command=%+v err=%v", command, err)
	}
	if _, err := parseCommand(map[string]any{
		"type": "move_to_reserve", "registrationId": "17", "targetId": "18", "issuePacket": true, "unknown": true,
	}, false); err == nil {
		t.Fatal("unknown command field was accepted")
	}
}

func TestTransitionsKeepPacketAndParticipationIndependent(t *testing.T) {
	person := &Person{ID: "person-1", FirstName: "Иван", LastName: "Тестов", BirthDate: "2000-02-29", Gender: "male"}
	row := Registration{ID: "17", EventID: "42", RaceID: "5", Bib: "0017", EPC: "000a", Person: person, Status: "registered"}
	value := true
	changes, err := Apply([]Registration{row}, Command{Type: "set_dns", RegistrationID: "17", Value: &value})
	if err != nil {
		t.Fatal(err)
	}
	if changes[0].After.Status != "dns" || changes[0].After.Issued {
		t.Fatalf("unexpected state: %+v", changes[0].After)
	}
	row.Issued = true
	changes, err = Apply([]Registration{row}, Command{Type: "set_dns", RegistrationID: "17", Value: &value})
	if err != nil {
		t.Fatal(err)
	}
	if !changes[0].After.Issued {
		t.Fatal("DNS must not undo issuance")
	}
}

func TestReleaseToReserveClearsLegacyTransferAndRejectsTiming(t *testing.T) {
	person := &Person{ID: "person-1", FirstName: "Иван", LastName: "Тестов"}
	row := Registration{ID: "17", EventID: "42", RaceID: "5", Bib: "0017", EPC: "000a", Person: person, Status: "dns"}
	changes, err := Apply([]Registration{row}, Command{Type: "release_to_reserve", RegistrationID: "17"})
	if err != nil {
		t.Fatal(err)
	}
	after := changes[0].After
	if after.Person != nil || !after.Reserve || after.Issued || after.Status != "registered" || after.Bib != row.Bib || after.EPC != row.EPC {
		t.Fatalf("unexpected reserve state: %+v", after)
	}
	target := "18"
	row.Issued = true
	row.TransferredTo = &target
	changes, err = Apply([]Registration{row}, Command{Type: "release_to_reserve", RegistrationID: "17"})
	if err != nil || changes[0].After.TransferredTo != nil || changes[0].After.Issued || !changes[0].After.Reserve {
		t.Fatalf("legacy transfer was not released: changes=%+v err=%v", changes, err)
	}
	row.HasTimingEvidence = true
	if _, err := Apply([]Registration{row}, Command{Type: "release_to_reserve", RegistrationID: "17"}); err == nil {
		t.Fatal("timed release was accepted")
	}
}

func TestReplacePersonRegistersNewParticipantOnReserveWithoutChangingPacket(t *testing.T) {
	reserve := Registration{ID: "18", EventID: "42", RaceID: "5", Bib: "0132", EPC: "000b", Reserve: true, Status: "registered"}
	person := &Person{ID: "local-person", FirstName: "Анна", LastName: "Новая", BirthDate: "1995-03-04", Gender: "female"}
	issued := true
	changes, err := Apply([]Registration{reserve}, Command{
		Type: "replace_person", RegistrationID: reserve.ID, Person: person, IssuePacket: &issued,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].After.Person == nil || changes[0].After.Person.ID != person.ID ||
		changes[0].After.Reserve || !changes[0].After.Issued || changes[0].After.Bib != reserve.Bib ||
		changes[0].After.EPC != reserve.EPC || changes[0].After.RaceID != reserve.RaceID {
		t.Fatalf("unexpected reserve registration: %+v", changes)
	}
}

func TestMoveToReserveAllowsSameRaceAndReleasesSource(t *testing.T) {
	person := &Person{ID: "person-1", FirstName: "Иван", LastName: "Тестов"}
	source := Registration{ID: "17", EventID: "42", RaceID: "5", Bib: "131", EPC: "a", Person: person, Issued: true, Status: "registered"}
	target := Registration{ID: "18", EventID: "42", RaceID: "5", Bib: "132", EPC: "b", Reserve: true, Status: "registered"}
	issued := true
	changes, err := Apply([]Registration{source, target}, Command{
		Type: "move_to_reserve", RegistrationID: "17", TargetID: "18", IssuePacket: &issued,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 || changes[0].After.Person != nil || !changes[0].After.Reserve || changes[0].After.Issued ||
		changes[1].After.Person == nil || changes[1].After.Person.ID != person.ID || !changes[1].After.Issued {
		t.Fatalf("unexpected move: %+v", changes)
	}
	reversed, err := ReverseMove([]Registration{changes[0].After, changes[1].After}, changes, "Ошибка")
	if err != nil || !equalRegistration(reversed[0].After, source) || !equalRegistration(reversed[1].After, target) {
		t.Fatalf("reverse=%+v err=%v", reversed, err)
	}
}

func TestFeedPageRequiresContiguousCursorAndSeparatesTimingEvidence(t *testing.T) {
	data, err := os.ReadFile("testdata/packet-issuance-operations-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Operation map[string]any `json:"operation"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	operationChanges := fixture.Operation["changes"].([]any)
	feedChanges := make([]any, 0, len(operationChanges))
	for _, raw := range operationChanges {
		encoded, _ := json.Marshal(raw)
		var change map[string]any
		_ = json.Unmarshal(encoded, &change)
		delete(change["before"].(map[string]any), "hasTimingEvidence")
		delete(change["after"].(map[string]any), "hasTimingEvidence")
		feedChanges = append(feedChanges, change)
	}
	scope := fixture.Operation["scopeId"].(string)
	page := map[string]any{
		"schemaVersion": 1, "scopeId": scope,
		"cursor": map[string]any{"after": "16", "next": "17", "head": "17", "hasMore": false},
		"actions": []any{map[string]any{
			"actionId": fixture.Operation["operationId"], "kind": "operation", "sequence": "17",
			"recordedAt": "2026-09-13T08:00:00.123Z", "sourceCode": "pwa.packet_issuance",
			"outcome": "applied", "code": nil, "operation": fixture.Operation, "changes": feedChanges,
		}},
	}
	wire, _ := json.Marshal(page)
	parsed, err := ParseFeedPage(wire, scope, "16")
	if err != nil || parsed.Cursor.Next != "17" || len(parsed.Actions) != 1 || parsed.Actions[0].Operation == nil {
		t.Fatalf("page=%+v err=%v", parsed, err)
	}
	if parsed.Actions[0].Changes[0].After.HasTimingEvidence {
		t.Fatal("feed invented timing evidence")
	}

	page["cursor"].(map[string]any)["next"] = "18"
	wire, _ = json.Marshal(page)
	if _, err := ParseFeedPage(wire, scope, "16"); err == nil {
		t.Fatal("cursor gap was accepted")
	}
}

func TestCanonicalPacketFeedFixtureChecksum(t *testing.T) {
	data, err := os.ReadFile("testdata/packet-issuance-feed-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprintf("%x", sha256.Sum256(data)), "ccd78c5ec3a320f34068aa91481ba8131f97dcbe99dd4da64833bfe4717dc5bb"; got != want {
		t.Fatalf("canonical fixture checksum=%s want=%s", got, want)
	}
	page, err := ParseFeedPage(data, "site:22222222-2222-4222-8222-222222222222:621632", "16")
	if err != nil || len(page.Actions) != 2 || page.Cursor.Next != "18" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

func TestCanonicalBootstrapFixtureAndNetworkCursor(t *testing.T) {
	data, err := os.ReadFile("testdata/packet-issuance-bootstrap-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprintf("%x", sha256.Sum256(data)), "094fbf7718702c91d7153d1f42e4147fc97822daa45eeb0e140e9d446e08b368"; got != want {
		t.Fatalf("canonical fixture checksum=%s want=%s", got, want)
	}
	var fixture struct {
		Snapshot map[string]any `json:"snapshot"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	fixture.Snapshot["baselineId"] = "snapshot:094fbf7718702c91d7153d1f42e4147fc97822daa45eeb0e140e9d446e08b368"
	v1, _ := json.Marshal(fixture.Snapshot)
	parsed, err := ParseBootstrap(v1)
	if err != nil || parsed.SchemaVersion != 1 || parsed.FeedCursor != "0" || parsed.Event.ID != "621632" {
		t.Fatalf("v1=%+v err=%v", parsed, err)
	}

	fixture.Snapshot["schemaVersion"] = float64(2)
	fixture.Snapshot["feedCursor"] = "17"
	v2, _ := json.Marshal(fixture.Snapshot)
	parsed, err = ParseBootstrap(v2)
	if err != nil || parsed.SchemaVersion != 2 || parsed.FeedCursor != "17" {
		t.Fatalf("v2=%+v err=%v", parsed, err)
	}

	for _, invalid := range []any{"017", "-1", float64(17), "9223372036854775808"} {
		fixture.Snapshot["feedCursor"] = invalid
		wire, _ := json.Marshal(fixture.Snapshot)
		if _, err := ParseBootstrap(wire); err == nil {
			t.Fatalf("invalid network cursor accepted: %#v", invalid)
		}
	}
}
