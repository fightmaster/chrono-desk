package service

import (
	"bytes"
	"context"
	"encoding/json"
	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	timing "gitlab.com/fightmaster1/timing-core"
	"io"
	"log"
	"testing"
)

func TestDualTimingClassifiesAndShiftsOnlyAssignedInputs(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	importFixture(t, store)
	_, err := store.DB().Exec(`UPDATE members SET start_time_ms=1780812005000, start_time_source='observation' WHERE id='mem-1'`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.DB().Exec(`UPDATE members SET start_time_ms=1780812030000, start_time_source='unknown' WHERE id='mem-2'`)
	if err != nil {
		t.Fatal(err)
	}
	req := EditRequest{Entity: "race", EntityID: "race-10k", Field: "timing_basis", Value: json.RawMessage(`"chip"`)}
	if _, err := ApplyEdit(ctx, store, req); err == nil {
		t.Fatal("unknown starts accepted without confirmation")
	}
	req.ConfirmLegacyAssigned = true
	if _, err := ApplyEdit(ctx, store, req); err != nil {
		t.Fatal(err)
	}
	a, err := store.GetMember(ctx, "mem-1")
	if err != nil {
		t.Fatal(err)
	}
	if a.StartTimeMs != nil {
		t.Fatal("measured start remained an assigned input")
	}
	_, err = ApplyEdit(ctx, store, EditRequest{Entity: "race", EntityID: "race-10k", Field: "shift_assigned_starts", Value: json.RawMessage(`1`)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ApplyEdit(ctx, store, EditRequest{Entity: "race", EntityID: "race-10k", Field: "started_at_ms", Value: json.RawMessage(`1780812300000`)})
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.GetMember(ctx, "mem-2")
	if err != nil {
		t.Fatal(err)
	}
	if b.StartTimeMs == nil || *b.StartTimeMs != 1780812330000 {
		t.Fatalf("assigned schedule = %#v", b.StartTimeMs)
	}
	// Journal holds absolute times; repeated replay cannot apply the delta again.
	if _, err := ReapplyLocalEdits(ctx, store); err != nil {
		t.Fatal(err)
	}
	b, err = store.GetMember(ctx, "mem-2")
	if err != nil {
		t.Fatal(err)
	}
	if b.StartTimeMs == nil || *b.StartTimeMs != 1780812330000 {
		t.Fatal("journal applied schedule delta twice")
	}
}

func TestDualReplayPersistsAssignedAndMeasuredSeparately(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.UpsertEvent(ctx, domain.Event{ID: "dual", Name: "Synthetic"}); err != nil {
		t.Fatal(err)
	}
	const gun int64 = 1750000000035
	if err := store.UpsertRace(ctx, domain.Race{ID: "r", EventID: "dual", Format: domain.FormatFixedDistance, StartedAtMs: ptr(gun), TimingBasis: timing.TimingChip, TimingSettingsPresent: true}); err != nil {
		t.Fatal(err)
	}
	for _, cp := range []domain.Checkpoint{
		{ID: "s", EventID: "dual", RaceID: "r", Type: domain.CheckpointStart, Sort: 1, Board: "S", SinceOffsetSeconds: ptr(0), UntilOffsetSeconds: ptr(60)},
		{ID: "f", EventID: "dual", RaceID: "r", Type: domain.CheckpointFinish, Sort: 2, Board: "F", SinceOffsetSeconds: ptr(60)},
	} {
		if err := store.UpsertCheckpoint(ctx, cp); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.UpsertMember(ctx, domain.Member{ID: "m", EventID: "dual", RaceID: "r", EPC: sptr("TAG"), StartTimeMs: ptr(gun + 1000), StartTimeSource: timing.StartTimeManual, StartProvenancePresent: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRfidLogs(ctx, []domain.RfidLog{
		{ID: "start", EventID: "dual", EPC: "TAG", Board: "S", TimeMs: gun + 5000},
		{ID: "finish", EventID: "dual", EPC: "TAG", Board: "F", TimeMs: gun + 120000},
	}); err != nil {
		t.Fatal(err)
	}
	rec := NewRecounter(store, log.New(io.Discard, "", 0), false)
	for i := 0; i < 2; i++ {
		if _, err := rec.Recount(ctx, "dual", ""); err != nil {
			t.Fatal(err)
		}
		m, err := store.GetMember(ctx, "m")
		if err != nil {
			t.Fatal(err)
		}
		if m.StartTimeMs == nil || *m.StartTimeMs != gun+1000 || m.ChipStartTimeMs == nil || *m.ChipStartTimeMs != gun+5000 || m.ChipStartObservationID != "start" || m.CleanTime == nil || *m.CleanTime != "00:01:55" {
			t.Fatalf("dual stored member = %#v", m)
		}
	}
	raw, _, err := BuildEventExport(ctx, store, "dual")
	if err != nil {
		t.Fatal(err)
	}
	export, err := ParseEventExport(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if export.SchemaVersion != 5 {
		t.Fatal("dual export lost policy/provenance")
	}
	target := newTestStore(t)
	if _, err := NewEventImporter(target).Import(ctx, export); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRecounter(target, log.New(io.Discard, "", 0), false).Recount(ctx, "dual", ""); err != nil {
		t.Fatal(err)
	}
	restored, err := target.GetMember(ctx, "m")
	if err != nil {
		t.Fatal(err)
	}
	if restored.StartTimeMs == nil || *restored.StartTimeMs != gun+1000 || restored.ChipStartTimeMs == nil || *restored.ChipStartTimeMs != gun+5000 {
		t.Fatal("round trip conflated start inputs")
	}
	if err := applyManualFinish(ctx, store, "m", gun+130000); err != nil {
		t.Fatal(err)
	}
	manual, err := store.GetMember(ctx, "m")
	if err != nil {
		t.Fatal(err)
	}
	if manual.StartTimeMs == nil || *manual.StartTimeMs != gun+1000 || manual.ChipStartTimeMs == nil || *manual.ChipStartTimeMs != gun+5000 || manual.CleanTime == nil || *manual.CleanTime != "00:02:05" {
		t.Fatal("manual finish overwrote start or used wrong basis")
	}

}
