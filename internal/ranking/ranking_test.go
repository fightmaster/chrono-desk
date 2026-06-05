package ranking

import (
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

func ptr(v int64) *int64    { return &v }
func sptr(v string) *string { return &v }

// finished builds a finished FixedDistance member.
func finished(id, gender, category string, cleanMs int64) domain.Member {
	start := int64(1_000_000)
	finish := start + cleanMs
	clean := "x" // presence matters for eligibility, value comes from times
	m := domain.Member{
		ID: id, StartTimeMs: &start, FinishTimeMs: &finish, CleanTime: &clean,
	}
	if gender != "" {
		m.Gender = sptr(gender)
	}
	if category != "" {
		m.CategoryID = sptr(category)
	}
	return m
}

func TestProtocolOrdersByCleanTimeAndAssignsPlaces(t *testing.T) {
	race := domain.Race{Format: domain.FormatFixedDistance}
	members := []domain.Member{
		finished("slow", "male", "", 50_000),
		finished("fast", "male", "", 30_000),
		{ID: "dnf", Status: domain.StatusDNF, Gender: sptr("male")},
		finished("mid", "female", "", 40_000),
		{ID: "not-finished"}, // no times → absent
	}

	rows := Protocol(race, members)

	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4 (unfinished member absent)", len(rows))
	}
	wantOrder := []string{"fast", "mid", "slow", "dnf"}
	for i, want := range wantOrder {
		if rows[i].Member.ID != want {
			t.Errorf("rows[%d] = %s, want %s", i, rows[i].Member.ID, want)
		}
	}

	if rows[0].Place == nil || *rows[0].Place != 1 {
		t.Errorf("fast place = %v, want 1", rows[0].Place)
	}
	if rows[2].Place == nil || *rows[2].Place != 3 {
		t.Errorf("slow place = %v, want 3", rows[2].Place)
	}
	if rows[3].Place != nil {
		t.Errorf("dnf place = %v, want nil", rows[3].Place)
	}
	if rows[3].Status != "dnf" {
		t.Errorf("dnf status = %q", rows[3].Status)
	}

	// Gender places: men fast=1, slow=2; women mid=1.
	if rows[0].GenderPlace == nil || *rows[0].GenderPlace != 1 {
		t.Errorf("fast gender place = %v, want 1", rows[0].GenderPlace)
	}
	if rows[1].GenderPlace == nil || *rows[1].GenderPlace != 1 {
		t.Errorf("mid gender place = %v, want 1 (first woman)", rows[1].GenderPlace)
	}
	if rows[2].GenderPlace == nil || *rows[2].GenderPlace != 2 {
		t.Errorf("slow gender place = %v, want 2", rows[2].GenderPlace)
	}

	if rows[0].CleanTimeMs == nil || *rows[0].CleanTimeMs != 30_000 {
		t.Errorf("fast clean = %v, want 30000", rows[0].CleanTimeMs)
	}
}

func TestProtocolStableOrderOnEqualTimes(t *testing.T) {
	race := domain.Race{Format: domain.FormatFixedDistance}
	members := []domain.Member{
		finished("first-in", "", "", 30_000),
		finished("second-in", "", "", 30_000),
	}

	rows := Protocol(race, members)
	if rows[0].Member.ID != "first-in" || rows[1].Member.ID != "second-in" {
		t.Errorf("equal times must keep input order, got %s, %s", rows[0].Member.ID, rows[1].Member.ID)
	}
}

func TestStandardCategoryPlaces(t *testing.T) {
	race := domain.Race{Format: domain.FormatFixedDistance}
	members := []domain.Member{
		finished("a", "male", "catX", 10_000),
		finished("b", "male", "catX", 20_000),
		finished("c", "male", "", 15_000), // no category → no category place
	}

	rows := Protocol(race, members)

	got := map[string]*int{}
	for _, r := range rows {
		got[r.Member.ID] = r.CategoryPlace
	}
	if got["a"] == nil || *got["a"] != 1 {
		t.Errorf("a category place = %v, want 1", got["a"])
	}
	if got["b"] == nil || *got["b"] != 2 {
		t.Errorf("b category place = %v, want 2", got["b"])
	}
	if got["c"] != nil {
		t.Errorf("c category place = %v, want nil", got["c"])
	}
}

func TestExcludeTopByGenderCategoryPlaces(t *testing.T) {
	race := domain.Race{Format: domain.FormatFixedDistance, CategoryExcludesTopByGender: true}
	// Five men in one category; overall top-3 must vanish from category
	// standings so #4 and #5 take category gold and silver.
	members := []domain.Member{
		finished("m1", "male", "catX", 10_000),
		finished("m2", "male", "catX", 11_000),
		finished("m3", "male", "catX", 12_000),
		finished("m4", "male", "catX", 13_000),
		finished("m5", "male", "catX", 14_000),
		finished("w1", "female", "catY", 20_000),
		finished("w2", "female", "catY", 21_000),
	}

	rows := Protocol(race, members)

	got := map[string]*int{}
	for _, r := range rows {
		got[r.Member.ID] = r.CategoryPlace
	}

	for _, id := range []string{"m1", "m2", "m3"} {
		if got[id] != nil {
			t.Errorf("%s category place = %v, want nil (overall medalist)", id, *got[id])
		}
	}
	if got["m4"] == nil || *got["m4"] != 1 {
		t.Errorf("m4 category place = %v, want 1", got["m4"])
	}
	if got["m5"] == nil || *got["m5"] != 2 {
		t.Errorf("m5 category place = %v, want 2", got["m5"])
	}
	// Only two women: both inside the gender top-3 → excluded.
	if got["w1"] != nil || got["w2"] != nil {
		t.Errorf("w1/w2 category places = %v/%v, want nil/nil", got["w1"], got["w2"])
	}
}
