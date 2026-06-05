package service

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

// BuildEventExport renders the event back into the schema_version 1 contract
// (docs/event-export-format.md) — the JSON backup. The current local state is
// exported as-is: edited start times, judge-disabled logs, locally created
// members/checkpoints all carry over, so importing the file into another
// chrono-desk plus a recount reproduces the protocol exactly. Derived data
// (results, member_results) is intentionally absent — recountable; for a
// byte-perfect copy including the edit journal use the .chrono snapshot.
func BuildEventExport(ctx context.Context, store *sqlite.Store, eventID string) ([]byte, string, error) {
	event, err := store.GetEvent(ctx, eventID)
	if err != nil {
		return nil, "", err
	}

	export := EventExport{
		SchemaVersion: 1,
		ExportedAt:    time.Now().UTC().Format(time.RFC3339),
		Timezone:      event.Timezone,
		Event: exportEvent{
			ID: event.ID, Name: event.Name, Slug: event.Slug, Date: event.Date,
		},
		Laps:        []exportLap{},
		Races:       []exportRace{},
		Categories:  []exportCategory{},
		Checkpoints: []exportCheckpoint{},
		Members:     []exportMember{},
		RfidLogs:    []exportRfidLog{},
	}

	laps, err := store.ListLaps(ctx)
	if err != nil {
		return nil, "", err
	}
	for _, l := range laps {
		export.Laps = append(export.Laps, exportLap(l))
	}

	races, err := store.ListRaces(ctx, eventID)
	if err != nil {
		return nil, "", err
	}
	for _, r := range races {
		export.Races = append(export.Races, exportRace{
			ID: r.ID, EventID: r.EventID, Name: r.Name, Date: r.Date,
			StartedAt: msToISO(r.StartedAtMs), LapID: r.LapID, Format: string(r.Format),
			TimeLimitSeconds: r.TimeLimitSeconds, CategoryExcludesTopByGender: r.CategoryExcludesTopByGender,
		})
	}

	categories, err := store.ListCategories(ctx)
	if err != nil {
		return nil, "", err
	}
	for _, c := range categories {
		export.Categories = append(export.Categories, exportCategory(c))
	}

	checkpoints, err := store.ListCheckpointsFullByEvent(ctx, eventID)
	if err != nil {
		return nil, "", err
	}
	for _, cp := range checkpoints {
		export.Checkpoints = append(export.Checkpoints, exportCheckpoint{
			ID: cp.ID, EventID: cp.EventID, RaceID: cp.RaceID, Name: cp.Name,
			Type: int(cp.Type), Sort: cp.Sort, Board: cp.Board,
			Since: msToISO(cp.SinceMs), SinceOffsetSeconds: cp.SinceOffsetSeconds,
			SleepAfterPrevSeconds: cp.SleepAfterPrevSeconds,
		})
	}

	members, err := store.ListMembersFullByEvent(ctx, eventID)
	if err != nil {
		return nil, "", err
	}
	for _, m := range members {
		export.Members = append(export.Members, exportMember{
			ID: m.ID, EventID: m.EventID, RaceID: m.RaceID, CategoryID: m.CategoryID,
			Number: m.Number, EPC: m.EPC, RFID: m.RFID,
			FirstName: m.FirstName, LastName: m.LastName, Gender: m.Gender, DOB: m.DOB,
			City: m.City, Team: m.Team, Status: int(m.Status),
			StartTime: msToISO(m.StartTimeMs), FinishTime: msToISO(m.FinishTimeMs), CleanTime: m.CleanTime,
		})
	}

	logs, err := store.ListRfidLogs(ctx, eventID)
	if err != nil {
		return nil, "", err
	}
	for _, l := range logs {
		export.RfidLogs = append(export.RfidLogs, exportRfidLog{
			ID: l.ID, EventID: l.EventID, Status: l.Status, Number: l.Number,
			Time: l.TimeMs, Ant: l.Ant, EPC: l.EPC, RSSI: l.RSSI, Board: l.Board,
			DisabledAt: msToISO(l.DisabledAt),
		})
	}

	data, err := json.Marshal(export)
	if err != nil {
		return nil, "", fmt.Errorf("encode export: %w", err)
	}
	name := fmt.Sprintf("event-%s-backup-%s.json", safeFileName(event.Slug+"-"+event.ID), time.Now().Format("2006-01-02-1504"))
	return data, name, nil
}

// SnapshotEvent copies the event database into a timestamped .chrono file —
// the full backup (results and the local-changes journal included). Restore:
// drop the file back into the events directory.
func SnapshotEvent(ctx context.Context, store *sqlite.Store, eventID, destDir string) (string, error) {
	event, err := store.GetEvent(ctx, eventID)
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("event-%s-backup-%s.chrono", safeFileName(event.Slug+"-"+event.ID), time.Now().Format("2006-01-02-1504"))
	path := filepath.Join(destDir, name)
	if err := store.SnapshotTo(ctx, path); err != nil {
		return "", err
	}
	return path, nil
}

func msToISO(ms *int64) *string {
	if ms == nil {
		return nil
	}
	s := time.UnixMilli(*ms).UTC().Format("2006-01-02T15:04:05.000Z07:00")
	return &s
}
