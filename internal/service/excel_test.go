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
