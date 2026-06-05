package service

import (
	"context"
	"encoding/json"
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
