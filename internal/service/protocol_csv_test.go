package service

import (
	"bytes"
	"context"
	"encoding/csv"
	"strings"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

func TestBuildProtocolCSV(t *testing.T) {
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
	winFinish := start + 30*60*1000
	secondFinish := start + 32*60*1000 + 500
	clean := "x"
	dob := "1990-05-01"
	members := []domain.Member{
		{ID: "m1", EventID: "ev1", RaceID: "r1", Number: ptr(101), FirstName: "Иван", LastName: "Петров",
			Gender: sptr("male"), CategoryID: sptr("cat1"), DOB: &dob,
			StartTimeMs: &start, FinishTimeMs: &winFinish, CleanTime: &clean, Team: sptr("=unsafe")},
		{ID: "m2", EventID: "ev1", RaceID: "r1", Number: ptr(102), FirstName: "Пётр", LastName: "Сидоров",
			Gender: sptr("male"), CategoryID: sptr("cat1"),
			StartTimeMs: &start, FinishTimeMs: &secondFinish, CleanTime: &clean},
		{ID: "m3", EventID: "ev1", RaceID: "r1", Number: ptr(103), FirstName: "Анна", LastName: "Иванова",
			Gender: sptr("female"), Status: domain.StatusDNS},
	}
	for _, member := range members {
		if err := store.UpsertMember(ctx, member); err != nil {
			t.Fatal(err)
		}
	}

	data, name, err := BuildProtocolCSV(ctx, store, "r1")
	if err != nil {
		t.Fatalf("build CSV: %v", err)
	}
	if !strings.HasSuffix(name, ".csv") {
		t.Fatalf("file name=%q", name)
	}
	if !bytes.HasPrefix(data, []byte("\xEF\xBB\xBF")) || !bytes.Contains(data, []byte("\r\n")) {
		t.Fatal("CSV must use UTF-8 BOM and CRLF")
	}
	rows := parseProtocolCSV(t, data)
	if len(rows) != 4 || len(rows[0]) != len(protocolHeaders) {
		t.Fatalf("rows=%d columns=%d", len(rows), len(rows[0]))
	}
	if rows[0][0] != "Абс" || rows[0][13] != "Отставание" || rows[0][14] != "Очки" {
		t.Fatalf("headers=%v", rows[0])
	}
	if rows[1][0] != "1" || rows[1][4] != "Петров" || rows[1][12] != "00:30:00" || rows[1][13] != "" {
		t.Fatalf("winner row=%v", rows[1])
	}
	if rows[1][6] != "01.05.1990" || rows[1][7] != "М" || rows[1][8] != "M18-39" || rows[1][9] != "'=unsafe" {
		t.Fatalf("winner metadata=%v", rows[1])
	}
	if rows[2][0] != "2" || rows[2][13] != "+02:00.500" {
		t.Fatalf("second row=%v", rows[2])
	}
	if rows[3][0] != "" || rows[3][11] != "Не стартовал" || rows[3][12] != "" {
		t.Fatalf("DNS row=%v", rows[3])
	}
}

func TestBuildProtocolCSVTimeLimitedGap(t *testing.T) {
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
	for _, member := range []domain.Member{
		{ID: "m1", EventID: "ev1", RaceID: "r1", Number: ptr(1), FirstName: "A", LastName: "Победитель", Gender: sptr("male"), StartTimeMs: &start},
		{ID: "m2", EventID: "ev1", RaceID: "r1", Number: ptr(2), FirstName: "B", LastName: "Второй", Gender: sptr("male"), StartTimeMs: &start},
	} {
		if err := store.UpsertMember(ctx, member); err != nil {
			t.Fatal(err)
		}
	}
	insert := func(member string, timeMs int64) {
		t.Helper()
		if _, err := store.DB().ExecContext(ctx,
			`INSERT INTO results (event_id, race_id, member_id, checkpoint_id, rfid_log_id, time_ms, number)
			 VALUES ('ev1','r1',?, 'cp1', NULL, ?, NULL)`, member, timeMs); err != nil {
			t.Fatal(err)
		}
	}
	insert("m1", start+30*60*1000)
	insert("m2", start+32*60*1000)

	data, _, err := BuildProtocolCSV(ctx, store, "r1")
	if err != nil {
		t.Fatalf("build CSV: %v", err)
	}
	rows := parseProtocolCSV(t, data)
	if rows[1][0] != "1" || rows[1][4] != "Победитель" || rows[1][12] != "00:30:00" || rows[1][13] != "" {
		t.Fatalf("winner row=%v", rows[1])
	}
	if rows[2][0] != "2" || rows[2][13] != "+02:00.000" {
		t.Fatalf("second row=%v", rows[2])
	}
}

func parseProtocolCSV(t *testing.T, data []byte) [][]string {
	t.Helper()
	r := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))))
	r.Comma = ';'
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
