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
