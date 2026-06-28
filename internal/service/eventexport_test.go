package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"path/filepath"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

// JSON backup round-trip: export the current local state (including offline
// edits and a walk-in member), import it into a fresh database, recount —
// the protocol must be identical.
func TestEventExportRoundTrip(t *testing.T) {
	src := newTestStore(t)
	ctx := context.Background()
	importFixture(t, src)
	rec := NewRecounter(src, log.New(io.Discard, "", 0), false)

	// Local flavor: shifted start + walk-in with a tag + disabled log.
	if _, err := ApplyEdit(ctx, src, EditRequest{
		Entity: "race", EntityID: "race-10k", Field: "started_at_ms", Value: json.RawMessage(`1780812300000`),
	}); err != nil {
		t.Fatal(err)
	}
	epc := "E280EEE"
	if _, _, err := CreateMember(ctx, src, "ev-100", CreateMemberRequest{
		RaceID: "race-10k", LastName: "Локальный", EPC: &epc, DOB: sptr("1992-07-19"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyEdit(ctx, src, EditRequest{
		Entity: "rfid_log", EntityID: "log-active", Field: "disabled_at", Value: json.RawMessage(`1780900000000`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := rec.Recount(ctx, "ev-100", ""); err != nil {
		t.Fatal(err)
	}
	srcProtocol, err := BuildProtocol(ctx, src, "race-10k")
	if err != nil {
		t.Fatal(err)
	}

	data, name, err := BuildEventExport(ctx, src, "ev-100")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if name == "" {
		t.Error("empty backup name")
	}

	// Fresh database ← backup → recount → identical protocol.
	dst := newTestStore(t)
	export, err := ParseEventExport(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("parse own export: %v", err)
	}
	if _, err := NewEventImporter(dst).Import(ctx, export); err != nil {
		t.Fatalf("import own export: %v", err)
	}
	if _, err := NewRecounter(dst, log.New(io.Discard, "", 0), false).Recount(ctx, "ev-100", ""); err != nil {
		t.Fatal(err)
	}
	dstProtocol, err := BuildProtocol(ctx, dst, "race-10k")
	if err != nil {
		t.Fatal(err)
	}

	srcJSON, _ := json.Marshal(srcProtocol)
	dstJSON, _ := json.Marshal(dstProtocol)
	if !bytes.Equal(srcJSON, dstJSON) {
		t.Errorf("protocol diverged after round-trip:\nsrc: %s\ndst: %s", srcJSON, dstJSON)
	}
}

// .chrono snapshot: the copy opens as a valid store with the journal inside.
func TestSnapshotEvent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	importFixture(t, store)
	if _, err := ApplyEdit(ctx, store, EditRequest{
		Entity: "race", EntityID: "race-10k", Field: "started_at_ms", Value: json.RawMessage(`1780812300000`),
	}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	path, err := SnapshotEvent(ctx, store, "ev-100", dir)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if filepath.Dir(path) != dir {
		t.Errorf("snapshot path = %s", path)
	}

	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer db.Close()
	copyStore, err := sqlite.New(db)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := copyStore.ListLocalChanges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].Field != "started_at_ms" {
		t.Errorf("snapshot journal = %+v, want the started_at edit", changes)
	}
	var members int
	if err := copyStore.DB().QueryRow(`SELECT COUNT(*) FROM members`).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if members != 2 {
		t.Errorf("snapshot members = %d, want 2", members)
	}
}
