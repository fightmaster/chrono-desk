package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

// Event export DTOs — the wire format produced by run5's `event:export`
// command, defined in docs/event-export-format.md. Datetimes are ISO 8601
// strings; rfid_logs.time is unix milliseconds.

type EventExport struct {
	SchemaVersion int                `json:"schema_version"`
	ExportedAt    string             `json:"exported_at"`
	Timezone      string             `json:"timezone"`
	Event         exportEvent        `json:"event"`
	Laps          []exportLap        `json:"laps"`
	Races         []exportRace       `json:"races"`
	Categories    []exportCategory   `json:"categories"`
	Checkpoints   []exportCheckpoint `json:"checkpoints"`
	Members       []exportMember     `json:"members"`
	RfidLogs      []exportRfidLog    `json:"rfid_logs"`
}

type exportEvent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
	Date string `json:"date"`
}

type exportLap struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
}

type exportRace struct {
	ID                          string  `json:"id"`
	EventID                     string  `json:"event_id"`
	Name                        string  `json:"name"`
	Date                        string  `json:"date"`
	StartedAt                   *string `json:"started_at"`
	LapID                       *string `json:"lap_id"`
	Format                      string  `json:"format"`
	TimeLimitSeconds            *int64  `json:"time_limit_seconds"`
	CategoryExcludesTopByGender bool    `json:"category_excludes_top_by_gender"`
}

type exportCategory struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Min    *int    `json:"min"`
	Max    *int    `json:"max"`
	Gender *string `json:"gender"`
}

type exportCheckpoint struct {
	ID                    string  `json:"id"`
	EventID               string  `json:"event_id"`
	RaceID                string  `json:"race_id"`
	Name                  string  `json:"name"`
	Type                  int     `json:"type"`
	Sort                  int64   `json:"sort"`
	Board                 string  `json:"board"`
	Since                 *string `json:"since"`
	SinceOffsetSeconds    *int64  `json:"since_offset_seconds"`
	SleepAfterPrevSeconds *int64  `json:"sleep_after_prev_seconds"`
}

type exportMember struct {
	ID         string  `json:"id"`
	EventID    string  `json:"event_id"`
	RaceID     string  `json:"race_id"`
	CategoryID *string `json:"category_id"`
	Number     *int64  `json:"number"`
	EPC        *string `json:"epc"`
	RFID       *string `json:"rfid"`
	FirstName  string  `json:"first_name"`
	LastName   string  `json:"last_name"`
	Gender     *string `json:"gender"`
	DOB        *string `json:"dob"`
	City       *string `json:"city"`
	Team       *string `json:"team"`
	Status     int     `json:"status"`
	StartTime  *string `json:"start_time"`
	FinishTime *string `json:"finish_time"`
	CleanTime  *string `json:"clean_time"`
}

type exportRfidLog struct {
	ID         string  `json:"id"`
	EventID    string  `json:"event_id"`
	Status     int     `json:"status"`
	Number     int64   `json:"number"`
	Time       int64   `json:"time"`
	Ant        int     `json:"ant"`
	EPC        string  `json:"epc"`
	RSSI       int     `json:"rssi"`
	Board      string  `json:"board"`
	DisabledAt *string `json:"disabled_at"`
}

// ImportStats reports what an event import touched.
type ImportStats struct {
	EventID     string `json:"event_id"`
	Races       int    `json:"races"`
	Categories  int    `json:"categories"`
	Checkpoints int    `json:"checkpoints"`
	Members     int    `json:"members"`
	RfidLogs    int    `json:"rfid_logs"`
}

// EventImporter applies a run5 event export to an event database. Re-import
// is an upsert: site-owned data overwrites local rows in place.
type EventImporter struct {
	store *sqlite.Store
}

func NewEventImporter(store *sqlite.Store) *EventImporter {
	return &EventImporter{store: store}
}

func ParseEventExport(r io.Reader) (*EventExport, error) {
	var export EventExport
	dec := json.NewDecoder(r)
	if err := dec.Decode(&export); err != nil {
		return nil, fmt.Errorf("parse event export: %w", err)
	}
	if export.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported schema_version %d (want 1)", export.SchemaVersion)
	}
	if export.Event.ID == "" {
		return nil, fmt.Errorf("event export has no event id")
	}
	return &export, nil
}

func (i *EventImporter) Import(ctx context.Context, export *EventExport) (ImportStats, error) {
	stats := ImportStats{EventID: export.Event.ID}

	if err := i.store.UpsertEvent(ctx, domain.Event{
		ID: export.Event.ID, Name: export.Event.Name, Slug: export.Event.Slug, Date: export.Event.Date,
	}); err != nil {
		return stats, err
	}

	for _, l := range export.Laps {
		if err := i.store.UpsertLap(ctx, domain.Lap(l)); err != nil {
			return stats, err
		}
	}

	for _, r := range export.Races {
		startedAt, err := parseTimeMs(r.StartedAt)
		if err != nil {
			return stats, fmt.Errorf("race %s started_at: %w", r.ID, err)
		}
		if err := i.store.UpsertRace(ctx, domain.Race{
			ID: r.ID, EventID: r.EventID, Name: r.Name, Date: r.Date,
			StartedAtMs: startedAt, LapID: r.LapID, Format: domain.RaceFormat(r.Format),
			TimeLimitSeconds: r.TimeLimitSeconds, CategoryExcludesTopByGender: r.CategoryExcludesTopByGender,
		}); err != nil {
			return stats, err
		}
		stats.Races++
	}

	for _, c := range export.Categories {
		if err := i.store.UpsertCategory(ctx, domain.Category(c)); err != nil {
			return stats, err
		}
		stats.Categories++
	}

	for _, cp := range export.Checkpoints {
		since, err := parseTimeMs(cp.Since)
		if err != nil {
			return stats, fmt.Errorf("checkpoint %s since: %w", cp.ID, err)
		}
		if err := i.store.UpsertCheckpoint(ctx, domain.Checkpoint{
			ID: cp.ID, EventID: cp.EventID, RaceID: cp.RaceID, Name: cp.Name,
			Type: domain.CheckpointType(cp.Type), Sort: cp.Sort, Board: cp.Board,
			SinceMs: since, SinceOffsetSeconds: cp.SinceOffsetSeconds, SleepAfterPrevSeconds: cp.SleepAfterPrevSeconds,
		}); err != nil {
			return stats, err
		}
		stats.Checkpoints++
	}

	for _, m := range export.Members {
		startMs, err := parseTimeMs(m.StartTime)
		if err != nil {
			return stats, fmt.Errorf("member %s start_time: %w", m.ID, err)
		}
		finishMs, err := parseTimeMs(m.FinishTime)
		if err != nil {
			return stats, fmt.Errorf("member %s finish_time: %w", m.ID, err)
		}
		if err := i.store.UpsertMember(ctx, domain.Member{
			ID: m.ID, EventID: m.EventID, RaceID: m.RaceID, CategoryID: m.CategoryID,
			Number: m.Number, EPC: m.EPC, RFID: m.RFID,
			FirstName: m.FirstName, LastName: m.LastName, Gender: m.Gender, DOB: m.DOB,
			City: m.City, Team: m.Team, Status: domain.MemberStatus(m.Status),
			StartTimeMs: startMs, FinishTimeMs: finishMs, CleanTime: m.CleanTime,
		}); err != nil {
			return stats, err
		}
		stats.Members++
	}

	logs := make([]domain.RfidLog, 0, len(export.RfidLogs))
	for _, l := range export.RfidLogs {
		disabledAt, err := parseTimeMs(l.DisabledAt)
		if err != nil {
			return stats, fmt.Errorf("rfid_log %s disabled_at: %w", l.ID, err)
		}
		logs = append(logs, domain.RfidLog{
			ID: l.ID, EventID: l.EventID, Status: l.Status, Number: l.Number,
			TimeMs: l.Time, Ant: l.Ant, EPC: l.EPC, RSSI: l.RSSI, Board: l.Board,
			DisabledAt: disabledAt,
		})
	}
	if err := i.store.UpsertRfidLogs(ctx, logs); err != nil {
		return stats, err
	}
	stats.RfidLogs = len(logs)

	return stats, nil
}

// parseTimeMs accepts RFC 3339 timestamps (with or without fractional
// seconds) and the plain `2006-01-02 15:04:05` form Laravel often emits
// (interpreted as UTC); returns unix milliseconds.
func parseTimeMs(s *string) (*int64, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, *s); err == nil {
			ms := t.UnixMilli()
			return &ms, nil
		}
	}
	return nil, fmt.Errorf("unparseable time %q", *s)
}
