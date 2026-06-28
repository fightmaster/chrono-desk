package service

import (
	"context"
	"io"
	"log"
	"strings"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

// On-site registration: a walk-in participant gets a local- member that
// matches already-imported logs after a recount, survives a re-import and
// leaves a journal trail.
func TestCreateMemberOnSite(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	importFixture(t, store)

	epc := "E280CCC"
	number := int64(999)
	memberID, res, err := CreateMember(ctx, store, "ev-100", CreateMemberRequest{
		RaceID: "race-10k", FirstName: "Олег", LastName: "Местный",
		Number: &number, EPC: &epc, Gender: sptrT("male"), DOB: sptrT("1985-03-12"),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.HasPrefix(memberID, "local-") {
		t.Errorf("member id = %s, want local- prefix", memberID)
	}
	if !res.RecountNeeded {
		t.Error("creation with EPC must demand a recount")
	}

	// Walk-in's tag was already read by the finish reader (flash import).
	// Finish checkpoint opens at 09:10 (+03); this read is at ~09:16.
	if err := store.UpsertRfidLogs(ctx, []domain.RfidLog{{
		ID: "walkin-read", EventID: "ev-100", EPC: epc,
		Board: "Feibot:U659", TimeMs: 1780813000000, Ant: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRecounter(store, log.New(io.Discard, "", 0), false).Recount(ctx, "ev-100", ""); err != nil {
		t.Fatal(err)
	}
	var finish *int64
	if err := store.DB().QueryRow(`SELECT finish_time_ms FROM members WHERE id = ?`, memberID).Scan(&finish); err != nil {
		t.Fatal(err)
	}
	if finish == nil {
		t.Error("walk-in member must get a finish from existing logs after recount")
	}

	// Duplicate bib and tag are rejected.
	if _, _, err := CreateMember(ctx, store, "ev-100", CreateMemberRequest{
		RaceID: "race-10k", LastName: "Дубль", Number: &number, DOB: sptrT("1985-03-12"),
	}); err == nil {
		t.Error("duplicate bib must be rejected")
	}
	if _, _, err := CreateMember(ctx, store, "ev-100", CreateMemberRequest{
		RaceID: "race-10k", LastName: "Дубль", EPC: &epc, DOB: sptrT("1985-03-12"),
	}); err == nil {
		t.Error("duplicate EPC must be rejected")
	}

	// Re-import of the site export must not wipe the local member.
	stats := importFixture(t, store)
	if stats.Members != 2 {
		t.Fatalf("export members = %d (fixture unchanged)", stats.Members)
	}
	var name string
	if err := store.DB().QueryRow(`SELECT last_name FROM members WHERE id = ?`, memberID).Scan(&name); err != nil {
		t.Fatalf("local member must survive re-import: %v", err)
	}
	if name != "Местный" {
		t.Errorf("name = %q", name)
	}

	// Journal carries the creation entry.
	changes, err := store.ListLocalChanges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	created := 0
	for _, c := range changes {
		if c.Field == "_created" && c.EntityID == memberID {
			created++
		}
	}
	if created != 1 {
		t.Errorf("_created journal entries = %d, want 1", created)
	}
}

func sptrT(s string) *string { return &s }

// run5 parity: a locally created participant must carry a valid birth date.
func TestCreateMemberRequiresDOB(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	importFixture(t, store)

	base := func() CreateMemberRequest {
		return CreateMemberRequest{RaceID: "race-10k", FirstName: "Без", LastName: "Даты"}
	}

	// Missing DOB → rejected.
	if _, _, err := CreateMember(ctx, store, "ev-100", base()); err == nil {
		t.Error("member without dob must be rejected")
	}
	// Malformed DOB → rejected.
	bad := base()
	bad.DOB = sptrT("12.05.1990")
	if _, _, err := CreateMember(ctx, store, "ev-100", bad); err == nil {
		t.Error("non-ISO dob must be rejected")
	}
	// Valid ISO DOB → stored.
	ok := base()
	ok.DOB = sptrT("1990-05-01")
	id, _, err := CreateMember(ctx, store, "ev-100", ok)
	if err != nil {
		t.Fatalf("valid dob: %v", err)
	}
	var got *string
	if err := store.DB().QueryRow(`SELECT dob FROM members WHERE id = ?`, id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got == nil || *got != "1990-05-01" {
		t.Errorf("stored dob = %v, want 1990-05-01", got)
	}
}
