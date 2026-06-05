package service

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

func newTestStore(t *testing.T) *sqlite.Store {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "event.chrono"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := sqlite.New(db)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return store
}

// Reference id computed independently (md5sum over the concatenated string):
// md5("Feibot:U659" + "AABB01" + "1780650000123" + "1").
// 2026-06-05_12:00:00.123 Europe/Moscow == 1780650000123 unix ms.
const knownLogID = "82963c22858a7ff3715cd5002ace96ad"

func TestFeibotCsvImport(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.UpsertEvent(ctx, domain.Event{ID: "ev1", Name: "E"}); err != nil {
		t.Fatal(err)
	}

	csv := strings.Join([]string{
		"aabb01:2026-06-05_12:00:00.123,port=1,rssi=-60", // lowercase epc → uppercased
		"AABB02:2026-06-05_12:01:00.000,port=2,rssi=-55",
		"",                  // blank line skipped
		"not-a-feibot-line", // malformed → error entry
		"AABB01:2026-06-05_12:00:00.123,port=1,rssi=-60", // duplicate of the first read
	}, "\n")

	res, err := NewFeibotCsvImporter(store).Import(ctx, strings.NewReader(csv), "ev1", "U659", "Europe/Moscow")
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	if res.Parsed != 4 {
		t.Errorf("parsed = %d, want 4", res.Parsed)
	}
	if res.Inserted != 2 {
		t.Errorf("inserted = %d, want 2", res.Inserted)
	}
	if res.Duplicates != 1 {
		t.Errorf("duplicates = %d, want 1", res.Duplicates)
	}
	if len(res.Errors) != 1 || res.Errors[0].Line != 4 {
		t.Errorf("errors = %+v, want one error on line 4", res.Errors)
	}

	// The idempotency key must match the PHP/rfid-hub formula byte-for-byte.
	var board, epc string
	var timeMs int64
	var ant int
	err = store.DB().QueryRow(
		`SELECT board, epc, time_ms, ant FROM rfid_logs WHERE id = ?`, knownLogID).
		Scan(&board, &epc, &timeMs, &ant)
	if err != nil {
		t.Fatalf("log with reference id not found: %v", err)
	}
	if board != "Feibot:U659" || epc != "AABB01" || timeMs != 1780650000123 || ant != 1 {
		t.Errorf("log fields = %s %s %d %d", board, epc, timeMs, ant)
	}

	// Re-import of the same file: everything dedups (2 rows already stored
	// + the in-file duplicate).
	res, err = NewFeibotCsvImporter(store).Import(ctx, strings.NewReader(csv), "ev1", "U659", "Europe/Moscow")
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if res.Inserted != 0 || res.Duplicates != 3 {
		t.Errorf("re-import inserted=%d duplicates=%d, want 0/3", res.Inserted, res.Duplicates)
	}
}

func TestRfidLogIDFormula(t *testing.T) {
	got := RfidLogID("Feibot:U659", "AABB01", 1780650000123, 1)
	if got != knownLogID {
		t.Errorf("RfidLogID = %s, want %s", got, knownLogID)
	}
}
