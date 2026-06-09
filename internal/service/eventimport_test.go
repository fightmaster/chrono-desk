package service

import (
	"context"
	"os"
	"strings"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

func TestEventImportFromFixture(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	f, err := os.Open("testdata/event-export.json")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	export, err := ParseEventExport(f)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if export.Timezone != "Europe/Moscow" {
		t.Errorf("timezone = %q", export.Timezone)
	}

	stats, err := NewEventImporter(store).Import(ctx, export)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	want := ImportStats{EventID: "ev-100", Races: 1, Categories: 1, Checkpoints: 2, Members: 2, RfidLogs: 2}
	if stats != want {
		t.Errorf("stats = %+v, want %+v", stats, want)
	}

	// Race started_at converted to unix ms (2026-06-07T09:00:00+03:00).
	var startedAt int64
	if err := store.DB().QueryRow(`SELECT started_at_ms FROM races WHERE id = 'race-10k'`).Scan(&startedAt); err != nil {
		t.Fatal(err)
	}
	if startedAt != 1780812000000 {
		t.Errorf("started_at_ms = %d, want 1780812000000", startedAt)
	}

	// Disabled log carries its disabled_at; active one is NULL.
	var disabledCount int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM rfid_logs WHERE disabled_at IS NOT NULL`).Scan(&disabledCount); err != nil {
		t.Fatal(err)
	}
	if disabledCount != 1 {
		t.Errorf("disabled logs = %d, want 1", disabledCount)
	}

	// Member status (DNS) survives the round trip.
	var status int
	if err := store.DB().QueryRow(`SELECT status FROM members WHERE id = 'mem-2'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != 1 {
		t.Errorf("mem-2 status = %d, want 1 (DNS)", status)
	}

	// Re-import that disables the previously active log must update the row
	// (ADR-0007: judge corrections arrive via re-export).
	f2, err := os.Open("testdata/event-export.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()
	export2, err := ParseEventExport(f2)
	if err != nil {
		t.Fatal(err)
	}
	now := "2026-06-07T11:00:00+03:00"
	export2.RfidLogs[0].DisabledAt = &now
	if _, err := NewEventImporter(store).Import(ctx, export2); err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM rfid_logs WHERE disabled_at IS NOT NULL`).Scan(&disabledCount); err != nil {
		t.Fatal(err)
	}
	if disabledCount != 2 {
		t.Errorf("disabled logs after re-import = %d, want 2", disabledCount)
	}
}

// Validate-first: a malformed time anywhere in the export aborts the import
// before any DB write — no half-updated .chrono.
func TestImportRejectsMalformedTime(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	bad := "definitely-not-a-time"
	export := &EventExport{
		SchemaVersion: 1,
		Event:         exportEvent{ID: "ev-x", Name: "X"},
		Races:         []exportRace{{ID: "r-x", EventID: "ev-x", Name: "R", Format: "FixedDistance"}},
		Members:       []exportMember{{ID: "m-x", EventID: "ev-x", RaceID: "r-x", FirstName: "A", LastName: "B"}},
		// Valid event/race/member, but a broken disabled_at on the last entity.
		RfidLogs: []exportRfidLog{{ID: "l-x", EventID: "ev-x", Time: 1000, EPC: "E", Board: "Feibot:U1", DisabledAt: &bad}},
	}
	if _, err := NewEventImporter(store).Import(ctx, export); err == nil {
		t.Fatal("expected import to fail on malformed disabled_at")
	}
	assertAbsent(t, store, "events", "ev-x")
	assertAbsent(t, store, "members", "m-x")
}

// Atomicity: a mid-apply constraint violation (member referencing a race that
// isn't in the export) rolls back the whole transaction, including the event
// written earlier in it.
func TestImportRollsBackOnConstraintViolation(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	export := &EventExport{
		SchemaVersion: 1,
		Event:         exportEvent{ID: "ev-x", Name: "X"},
		Members:       []exportMember{{ID: "m-x", EventID: "ev-x", RaceID: "ghost", FirstName: "A", LastName: "B"}},
	}
	if _, err := NewEventImporter(store).Import(ctx, export); err == nil {
		t.Fatal("expected import to fail on FK violation (member → missing race)")
	}
	assertAbsent(t, store, "events", "ev-x")
}

func assertAbsent(t *testing.T, store *sqlite.Store, table, id string) {
	t.Helper()
	var n int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%s %q present after failed import (n=%d) — import not atomic", table, id, n)
	}
}

func TestParseEventExportRejectsUnknownSchema(t *testing.T) {
	_, err := ParseEventExport(strings.NewReader(`{"schema_version": 99, "event": {"id": "x"}}`))
	if err == nil || !strings.Contains(err.Error(), "schema_version") {
		t.Errorf("err = %v, want schema_version error", err)
	}
}
