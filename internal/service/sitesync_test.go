package service

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

// BuildSyncPayload gathers chip logs (incl. disabled_at), manual results,
// journal-collapsed member/race edits and local- walk-ins, and is deterministic
// (re-build → identical bytes) so an unchanged event won't re-push.
func TestBuildSyncPayload(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	importFixture(t, store)

	// Race start edit.
	mustEdit(t, store, EditRequest{Entity: "race", EntityID: "race-10k", Field: "started_at_ms", Value: json.RawMessage(`1780812300000`)})
	// Member status edited twice → last value wins on collapse.
	mustEdit(t, store, EditRequest{Entity: "member", EntityID: "mem-2", Field: "status", Value: json.RawMessage(`2`)})
	mustEdit(t, store, EditRequest{Entity: "member", EntityID: "mem-2", Field: "status", Value: json.RawMessage(`3`)})
	// Member start edit + a disabled log.
	mustEdit(t, store, EditRequest{Entity: "member", EntityID: "mem-1", Field: "start_time_ms", Value: json.RawMessage(`1780812000000`)})
	mustEdit(t, store, EditRequest{Entity: "rfid_log", EntityID: "log-active", Field: "disabled_at", Value: json.RawMessage(`1780900000000`)})

	// Manual finish (no chip) for mem-2.
	if _, err := ManualFinishClean(ctx, store, "ev-100", "mem-2", 600000); err != nil {
		t.Fatal(err)
	}
	// Offline walk-in.
	epc := "E280WALK"
	num := int64(555)
	localID, _, err := CreateMember(ctx, store, "ev-100", CreateMemberRequest{
		RaceID: "race-10k", FirstName: "Олег", LastName: "Местный", Number: &num, EPC: &epc, DOB: sptr("1980-01-02"),
	})
	if err != nil {
		t.Fatal(err)
	}

	data, summary, err := BuildSyncPayload(ctx, store, "ev-100", true)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	var p syncPayload
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !p.Overwrite || p.EventID != "ev-100" || p.SchemaVersion != syncSchemaVersion {
		t.Errorf("header = %+v", p)
	}

	// Disabled log present with disabled_at set.
	var disabled *syncRfidLog
	for i := range p.RfidLogs {
		if p.RfidLogs[i].ID == "log-active" {
			disabled = &p.RfidLogs[i]
		}
	}
	if disabled == nil || disabled.DisabledAt == nil {
		t.Errorf("log-active missing or not disabled: %+v", disabled)
	}

	// Manual result for mem-2 (site member → member_id set, not local_id).
	if len(p.ManualResults) != 1 {
		t.Fatalf("manual results = %d, want 1", len(p.ManualResults))
	}
	if mr := p.ManualResults[0]; mr.MemberRef.MemberID == nil || *mr.MemberRef.MemberID != "mem-2" || mr.MemberRef.LocalID != nil {
		t.Errorf("manual member_ref = %+v", mr.MemberRef)
	}

	// Member status edit collapsed to last value (3).
	var statusEdit *syncMemberEdit
	for i := range p.MemberEdits {
		if p.MemberEdits[i].MemberRef.MemberID != nil && *p.MemberEdits[i].MemberRef.MemberID == "mem-2" {
			statusEdit = &p.MemberEdits[i]
		}
	}
	if statusEdit == nil || string(statusEdit.Fields["status"]) != "3" {
		t.Errorf("mem-2 status edit = %+v", statusEdit)
	}

	// Walk-in present as a local member.
	if len(p.NewMembers) != 1 || p.NewMembers[0].LocalID != localID || p.NewMembers[0].EPC == nil || *p.NewMembers[0].EPC != "E280WALK" {
		t.Errorf("new_members = %+v", p.NewMembers)
	}

	// Race edit present.
	if len(p.RaceEdits) != 1 || p.RaceEdits[0].RaceID != "race-10k" || string(p.RaceEdits[0].Fields["started_at_ms"]) != "1780812300000" {
		t.Errorf("race_edits = %+v", p.RaceEdits)
	}

	if summary.RfidLogs == 0 || summary.ManualResults != 1 || summary.NewMembers != 1 {
		t.Errorf("summary = %+v", summary)
	}

	// Deterministic: rebuild yields identical bytes.
	data2, _, err := BuildSyncPayload(ctx, store, "ev-100", true)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, data2) {
		t.Error("payload not deterministic across rebuilds")
	}
}

// Deleting a manual result journals its natural key so the deletion syncs to
// run5; the payload then carries it in deleted_manual_results (and not in
// manual_results).
func TestBuildSyncPayloadDeletedManual(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	importFixture(t, store)

	res, err := ManualFinishClean(ctx, store, "ev-100", "mem-2", 600000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DeleteManualResult(ctx, store, "ev-100", res.ResultID); err != nil {
		t.Fatal(err)
	}

	data, _, err := BuildSyncPayload(ctx, store, "ev-100", true)
	if err != nil {
		t.Fatal(err)
	}
	var p syncPayload
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.ManualResults) != 0 {
		t.Errorf("manual_results = %d, want 0 (deleted)", len(p.ManualResults))
	}
	if len(p.DeletedManualResults) != 1 {
		t.Fatalf("deleted_manual_results = %d, want 1", len(p.DeletedManualResults))
	}
	d := p.DeletedManualResults[0]
	if d.MemberRef.MemberID == nil || *d.MemberRef.MemberID != "mem-2" || d.TimeMs == 0 {
		t.Errorf("deleted ref = %+v time=%d", d.MemberRef, d.TimeMs)
	}
}

// Offline (local-) checkpoints sync from the live table, not the journal — a
// local- checkpoint with no _created journal entry must still be emitted
// (regression: Т-7's finish checkpoint had only a sort edit journaled).
func TestBuildSyncPayloadCheckpointsFromTable(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	importFixture(t, store)

	id, _, err := CreateCheckpoint(ctx, store, "ev-100", CreateCheckpointRequest{
		RaceID: "race-10k", Name: "Финиш-2", Type: 3, Sort: 9, Board: "Feibot:U999",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Wipe the _created journal entry to simulate a checkpoint without it.
	if _, err := store.DB().ExecContext(ctx,
		`DELETE FROM local_changes WHERE entity='checkpoint' AND entity_id=? AND field='_created'`, id); err != nil {
		t.Fatal(err)
	}

	data, summary, err := BuildSyncPayload(ctx, store, "ev-100", true)
	if err != nil {
		t.Fatal(err)
	}
	if summary.CheckpointCreates != 1 {
		t.Fatalf("checkpoint_creates summary = %d, want 1", summary.CheckpointCreates)
	}
	var p syncPayload
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.CheckpointCreates) != 1 {
		t.Fatalf("checkpoint_creates = %d, want 1", len(p.CheckpointCreates))
	}
	cc := p.CheckpointCreates[0]
	if cc.ID != id || cc.RaceID != "race-10k" || cc.Type != 3 || cc.Sort != 9 || cc.Board != "Feibot:U999" {
		t.Errorf("checkpoint create = %+v", cc)
	}
}

func mustEdit(t *testing.T, store *sqlite.Store, req EditRequest) {
	t.Helper()
	if _, err := ApplyEdit(context.Background(), store, req); err != nil {
		t.Fatalf("edit %s.%s: %v", req.Entity, req.Field, err)
	}
}
