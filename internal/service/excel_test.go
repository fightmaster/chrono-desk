package service

import (
	"bytes"
	"context"
	"testing"

	"github.com/xuri/excelize/v2"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

func TestBuildProtocolXLSX(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if err := store.UpsertEvent(ctx, domain.Event{ID: "ev1", Name: "E"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRace(ctx, domain.Race{
		ID: "r1", EventID: "ev1", Name: "10 км", Format: domain.FormatFixedDistance,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertCategory(ctx, domain.Category{ID: "cat1", Name: "M18-39"}); err != nil {
		t.Fatal(err)
	}

	start := int64(1_000_000)
	winFinish := start + 30*60*1000          // 30:00
	secondFinish := start + 32*60*1000 + 500 // +02:00.500
	clean := "x"
	dob := "1990-05-01"
	members := []domain.Member{
		{ID: "m1", EventID: "ev1", RaceID: "r1", Number: ptr(101), FirstName: "Иван", LastName: "Петров",
			Gender: sptr("male"), CategoryID: sptr("cat1"), DOB: &dob,
			StartTimeMs: &start, FinishTimeMs: &winFinish, CleanTime: &clean},
		{ID: "m2", EventID: "ev1", RaceID: "r1", Number: ptr(102), FirstName: "Пётр", LastName: "Сидоров",
			Gender: sptr("male"), CategoryID: sptr("cat1"),
			StartTimeMs: &start, FinishTimeMs: &secondFinish, CleanTime: &clean},
		{ID: "m3", EventID: "ev1", RaceID: "r1", Number: ptr(103), FirstName: "Анна", LastName: "Иванова",
			Gender: sptr("female"), Status: domain.StatusDNS},
	}
	for _, m := range members {
		if err := store.UpsertMember(ctx, m); err != nil {
			t.Fatal(err)
		}
	}

	data, name, err := BuildProtocolXLSX(ctx, store, "r1")
	if err != nil {
		t.Fatalf("build xlsx: %v", err)
	}
	if name == "" {
		t.Error("empty file name")
	}

	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("open xlsx: %v", err)
	}
	defer f.Close()
	sheet := f.GetSheetName(0)

	cell := func(ref string) string {
		t.Helper()
		v, err := f.GetCellValue(sheet, ref)
		if err != nil {
			t.Fatalf("cell %s: %v", ref, err)
		}
		return v
	}

	if cell("A1") != "Абс" || cell("N1") != "Отставание" || cell("O1") != "Очки" {
		t.Errorf("headers = %q %q %q", cell("A1"), cell("N1"), cell("O1"))
	}

	// Winner row: place 1, time 00:30:00, no gap, dob in d.m.Y, gender М.
	if cell("A2") != "1" || cell("E2") != "Петров" || cell("M2") != "00:30:00" || cell("N2") != "" {
		t.Errorf("winner row = %s %s %s %q", cell("A2"), cell("E2"), cell("M2"), cell("N2"))
	}
	if cell("G2") != "01.05.1990" || cell("H2") != "М" || cell("I2") != "M18-39" {
		t.Errorf("winner dob/gender/cat = %q %q %q", cell("G2"), cell("H2"), cell("I2"))
	}

	// Second: gap +02:00.500 in run5's minutes form.
	if cell("A3") != "2" || cell("N3") != "+02:00.500" {
		t.Errorf("second row = %s gap %q", cell("A3"), cell("N3"))
	}

	// DNS: no place, label, empty time.
	if cell("A4") != "" || cell("L4") != "Не стартовал" || cell("M4") != "" {
		t.Errorf("dns row = %q %q %q", cell("A4"), cell("L4"), cell("M4"))
	}
}

// TimeLimited protocols render the in-window elapsed as the clean time; the
// «Отставание» gap (off CleanTimeMs) must populate too, not stay blank.
func TestBuildProtocolXLSXTimeLimitedGap(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if err := store.UpsertEvent(ctx, domain.Event{ID: "ev1", Name: "E"}); err != nil {
		t.Fatal(err)
	}
	limit := int64(6 * 3600)
	if err := store.UpsertRace(ctx, domain.Race{
		ID: "r1", EventID: "ev1", Name: "6 часов", Format: domain.FormatTimeLimited, TimeLimitSeconds: &limit,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertCheckpoint(ctx, domain.Checkpoint{
		ID: "cp1", EventID: "ev1", RaceID: "r1", Name: "круг", Type: domain.CheckpointMid, Sort: 2, Board: "Feibot:U1",
	}); err != nil {
		t.Fatal(err)
	}

	start := int64(1_000_000)
	for _, m := range []domain.Member{
		{ID: "m1", EventID: "ev1", RaceID: "r1", Number: ptr(1), FirstName: "A", LastName: "Победитель",
			Gender: sptr("male"), StartTimeMs: &start},
		{ID: "m2", EventID: "ev1", RaceID: "r1", Number: ptr(2), FirstName: "B", LastName: "Второй",
			Gender: sptr("male"), StartTimeMs: &start},
	} {
		if err := store.UpsertMember(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	// Last in-window pass: m1 at +30:00 (winner by elapsed), m2 at +32:00.
	insert := func(member string, tMs int64) {
		t.Helper()
		if _, err := store.DB().ExecContext(ctx,
			`INSERT INTO results (event_id, race_id, member_id, checkpoint_id, rfid_log_id, time_ms, number)
			 VALUES ('ev1','r1',?, 'cp1', NULL, ?, NULL)`, member, tMs); err != nil {
			t.Fatal(err)
		}
	}
	insert("m1", start+30*60*1000)
	insert("m2", start+32*60*1000)

	data, _, err := BuildProtocolXLSX(ctx, store, "r1")
	if err != nil {
		t.Fatalf("build xlsx: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("open xlsx: %v", err)
	}
	defer f.Close()
	sheet := f.GetSheetName(0)
	cell := func(ref string) string {
		t.Helper()
		v, _ := f.GetCellValue(sheet, ref)
		return v
	}

	// Winner: place 1, clean time = 30:00 elapsed, no gap.
	if cell("A2") != "1" || cell("E2") != "Победитель" || cell("M2") != "00:30:00" || cell("N2") != "" {
		t.Errorf("winner row = %s %s time=%q gap=%q", cell("A2"), cell("E2"), cell("M2"), cell("N2"))
	}
	// Second: gap +02:00.000 to the winner (the regression — was blank).
	if cell("A3") != "2" || cell("N3") != "+02:00.000" {
		t.Errorf("second row = %s gap=%q, want gap +02:00.000", cell("A3"), cell("N3"))
	}
}
