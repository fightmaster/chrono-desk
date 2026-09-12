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
