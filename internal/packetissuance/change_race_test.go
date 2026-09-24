package packetissuance

import "testing"

func TestChangeRaceKeepsUnnumberedRegistrationIdentity(t *testing.T) {
	row := Registration{
		ID: "700", EventID: "621632", RaceID: "100", Status: "registered",
		Person: &Person{ID: "member-person:700", FirstName: "Анна", LastName: "Иванова"},
	}
	changes, err := Apply([]Registration{row}, Command{Type: "change_race", RegistrationID: "700", RaceID: "101"})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].After.ID != row.ID || changes[0].After.RaceID != "101" ||
		changes[0].After.Person.ID != row.Person.ID || changes[0].After.Bib != "" {
		t.Fatalf("unexpected race change: %+v", changes)
	}
	for _, patch := range []func(*Registration){
		func(r *Registration) { r.Bib = "500" },
		func(r *Registration) { r.EPC = "ABC" },
		func(r *Registration) { r.Issued = true },
		func(r *Registration) { r.HasTimingEvidence = true },
	} {
		copy := row
		patch(&copy)
		if _, err := Apply([]Registration{copy}, Command{Type: "change_race", RegistrationID: "700", RaceID: "101"}); err == nil {
			t.Fatalf("unsafe race change accepted: %+v", copy)
		}
	}
}
