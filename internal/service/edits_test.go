package service

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

func importFixture(t *testing.T, store *sqlite.Store) ImportStats {
	t.Helper()
	f, err := os.Open("testdata/event-export.json")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()
	export, err := ParseEventExport(f)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	stats, err := NewEventImporter(store).Import(context.Background(), export)
	if err != nil {
		t.Fatalf("import fixture: %v", err)
	}
	return stats
}

func TestApplyEditJournalsAndUpdates(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	importFixture(t, store)

	// Delayed start: shift race start +30 minutes.
	res, err := ApplyEdit(ctx, store, EditRequest{
		Entity: "race", EntityID: "race-10k", Field: "started_at_ms",
		Value: json.RawMessage(`1780813800000`),
	})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if !res.RecountNeeded {
		t.Error("started_at edit must demand a recount")
	}

	var startedAt int64
	if err := store.DB().QueryRow(`SELECT started_at_ms FROM races WHERE id='race-10k'`).Scan(&startedAt); err != nil {
		t.Fatal(err)
	}
	if startedAt != 1780813800000 {
		t.Errorf("started_at = %d, want 1780813800000", startedAt)
	}

	changes, err := store.ListLocalChanges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Field != "started_at_ms" ||
		changes[0].OldValue != "1780812000000" || changes[0].NewValue != "1780813800000" {
		t.Errorf("journal = %+v", changes)
	}
}

// Clearing the bib number (value: null) must persist as SQL NULL and read back
// as a nil pointer — "номер не назначен".
func TestApplyEditClearsNumberToNull(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	importFixture(t, store)

	if _, err := ApplyEdit(ctx, store, EditRequest{
		Entity: "member", EntityID: "mem-1", Field: "number", Value: json.RawMessage(`null`),
	}); err != nil {
		t.Fatalf("clear number: %v", err)
	}

	var n *int64
	if err := store.DB().QueryRow(`SELECT number FROM members WHERE id='mem-1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != nil {
		t.Fatalf("number = %d, want NULL", *n)
	}
	m, err := store.GetMember(ctx, "mem-1")
	if err != nil {
		t.Fatal(err)
	}
	if m.Number != nil {
		t.Fatalf("GetMember number = %d, want nil", *m.Number)
	}
}

// Moving the race start shifts every member's start by the same delta — so a
// mass start follows and a staggered start keeps its gaps — and each shift is
// journaled (so it syncs to the site). The shift must also survive the recount
// that the edit triggers: a member with a finish but no start read keeps the
// shifted start instead of being re-derived.
func TestRaceStartShiftsMembersRelatively(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	importFixture(t, store)

	// Staggered start, 30s apart, anchored at the old race start.
	const oldStart, delta = int64(1780812000000), int64(120000) // +2 min
	if _, err := store.DB().ExecContext(ctx,
		`UPDATE members SET start_time_ms = ? WHERE id = 'mem-1'`, oldStart); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx,
		`UPDATE members SET start_time_ms = ? WHERE id = 'mem-2'`, oldStart+30000); err != nil {
		t.Fatal(err)
	}

	if _, err := ApplyEdit(ctx, store, EditRequest{
		Entity: "race", EntityID: "race-10k", Field: "started_at_ms",
		Value: json.RawMessage(`1780812120000`), // oldStart + delta
	}); err != nil {
		t.Fatalf("edit: %v", err)
	}

	assertStart := func(when, id string, want int64) {
		t.Helper()
		var got int64
		if err := store.DB().QueryRow(`SELECT start_time_ms FROM members WHERE id=?`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s: %s start = %d, want %d", when, id, got, want)
		}
	}
	// Both moved by +delta; the 30s gap is preserved.
	assertStart("after edit", "mem-1", oldStart+delta)
	assertStart("after edit", "mem-2", oldStart+30000+delta)

	changes, err := store.ListLocalChanges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][2]string{}
	for _, c := range changes {
		if c.Entity == "member" && c.Field == "start_time_ms" {
			got[c.EntityID] = [2]string{c.OldValue, c.NewValue}
		}
	}
	if len(got) != 2 ||
		got["mem-1"] != [2]string{"1780812000000", "1780812120000"} ||
		got["mem-2"] != [2]string{"1780812030000", "1780812150000"} {
		t.Errorf("journaled shifts = %v", got)
	}

	// The recount the UI runs after this edit must not undo the shift: mem-1 has
	// a finish read but no start read, so its shifted start survives.
	rc := NewRecounter(store, log.New(io.Discard, "", 0), false)
	if _, err := rc.Recount(ctx, "ev-100", "race-10k"); err != nil {
		t.Fatalf("recount: %v", err)
	}
	assertStart("after recount", "mem-1", oldStart+delta)
	assertStart("after recount", "mem-2", oldStart+30000+delta)
}

func TestApplyEditRejectsNonWhitelisted(t *testing.T) {
	store := newTestStore(t)
	importFixture(t, store)

	if _, err := ApplyEdit(context.Background(), store, EditRequest{
		Entity: "race", EntityID: "race-10k", Field: "id", Value: json.RawMessage(`"hack"`),
	}); err == nil {
		t.Fatal("editing race.id must be rejected")
	}
	if _, err := ApplyEdit(context.Background(), store, EditRequest{
		Entity: "results", EntityID: "1", Field: "time_ms", Value: json.RawMessage(`0`),
	}); err == nil {
		t.Fatal("editing results must be rejected")
	}
	if _, err := ApplyEdit(context.Background(), store, EditRequest{
		Entity: "race", EntityID: "race-10k", Field: "started_at_ms", Value: json.RawMessage(`"text"`),
	}); err == nil {
		t.Fatal("type mismatch must be rejected")
	}
}

// Conflict policy: a re-import refreshes site data but local edits win.
func TestReimportKeepsLocalEdits(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	importFixture(t, store)

	// Local offline changes: shifted start + judge-disabled log + DNS status.
	for _, req := range []EditRequest{
		{Entity: "race", EntityID: "race-10k", Field: "started_at_ms", Value: json.RawMessage(`1780813800000`)},
		{Entity: "rfid_log", EntityID: "log-active", Field: "disabled_at", Value: json.RawMessage(`1780900000000`)},
		{Entity: "member", EntityID: "mem-1", Field: "status", Value: json.RawMessage(`1`)},
	} {
		if _, err := ApplyEdit(ctx, store, req); err != nil {
			t.Fatalf("edit %s.%s: %v", req.Entity, req.Field, err)
		}
	}

	// The site export still carries the old start, an enabled log and status ok.
	stats := importFixture(t, store)
	if stats.LocalEditsReapplied != 3 {
		t.Errorf("reapplied = %d, want 3", stats.LocalEditsReapplied)
	}

	var startedAt int64
	var disabled *int64
	var status int
	if err := store.DB().QueryRow(`SELECT started_at_ms FROM races WHERE id='race-10k'`).Scan(&startedAt); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow(`SELECT disabled_at FROM rfid_logs WHERE id='log-active'`).Scan(&disabled); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow(`SELECT status FROM members WHERE id='mem-1'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if startedAt != 1780813800000 {
		t.Errorf("started_at after re-import = %d, local edit must win", startedAt)
	}
	if disabled == nil || *disabled != 1780900000000 {
		t.Errorf("disabled_at after re-import = %v, local edit must win", disabled)
	}
	if status != 1 {
		t.Errorf("status after re-import = %d, local edit must win", status)
	}
}

func TestMemberPassesView(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	importFixture(t, store)

	passes, err := LoadMemberPasses(ctx, store, "mem-1")
	if err != nil {
		t.Fatal(err)
	}
	if passes.LastName != "Petrov" || len(passes.Passes) != 1 {
		t.Fatalf("passes = %+v", passes)
	}
	p := passes.Passes[0]
	if p.LogID != "log-active" || p.CheckpointID != nil {
		t.Errorf("pass = %+v (no recount ran — no result expected)", p)
	}
}

// The run5 "top-3 excluded from categories" race setting is editable offline
// and takes effect without a recount (ranking-only).
func TestToggleExcludeTopByGender(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	importFixture(t, store) // race-10k has the flag ON in the fixture

	// Finished member with a category: winner of the race.
	start, finish := int64(1_000_000), int64(1_900_000)
	clean := "x"
	m, err := store.GetMember(ctx, "mem-1")
	if err != nil {
		t.Fatal(err)
	}
	m.StartTimeMs, m.FinishTimeMs, m.CleanTime = &start, &finish, &clean
	if err := store.UpsertMember(ctx, m); err != nil {
		t.Fatal(err)
	}

	protocol, err := BuildProtocol(ctx, store, "race-10k")
	if err != nil {
		t.Fatal(err)
	}
	if protocol.Rows[0].CategoryPlace != nil {
		t.Fatalf("flag ON: winner must be excluded from category standings, got %v", *protocol.Rows[0].CategoryPlace)
	}

	res, err := ApplyEdit(ctx, store, EditRequest{
		Entity: "race", EntityID: "race-10k",
		Field: "category_excludes_top_by_gender", Value: json.RawMessage(`0`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.RecountNeeded {
		t.Error("ranking-only flag must not demand a recount")
	}

	protocol, err = BuildProtocol(ctx, store, "race-10k")
	if err != nil {
		t.Fatal(err)
	}
	if protocol.Rows[0].CategoryPlace == nil || *protocol.Rows[0].CategoryPlace != 1 {
		t.Fatalf("flag OFF: winner must take category gold, got %v", protocol.Rows[0].CategoryPlace)
	}
}
