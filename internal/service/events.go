package service

import (
	"context"
	"io"
	"log"
	"sort"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

// EventService hosts event-level use cases on top of the infrastructure
// catalog: import, list, and store access for transports.
type EventService struct {
	catalog *sqlite.EventCatalog
	logger  *log.Logger
}

func NewEventService(catalog *sqlite.EventCatalog, logger *log.Logger) *EventService {
	return &EventService{catalog: catalog, logger: logger}
}

// NewEventManager is a compatibility helper for tests and older wiring. New
// application code should compose sqlite.EventCatalog in the app layer and then
// build EventService from it.
func NewEventManager(dataDir string, logger *log.Logger) (*EventService, error) {
	catalog, err := sqlite.NewEventCatalog(dataDir)
	if err != nil {
		return nil, err
	}
	return NewEventService(catalog, logger), nil
}

// EventInfo is a list entry for the UI.
type EventInfo struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Date              string `json:"date"`
	Timezone          string `json:"timezone"`
	File              string `json:"file"`
	RaceCount         int    `json:"race_count"`
	MemberCount       int    `json:"member_count"`
	UseRaceDateForAge bool   `json:"use_race_date_for_age"`
}

// Open returns the store for an existing event file.
func (s *EventService) Open(eventID string) (*sqlite.Store, error) {
	return s.catalog.Open(eventID)
}

// ImportExport parses a run5 event export and applies it to the event's
// database file, creating the file on first import. Local edits win.
func (s *EventService) ImportExport(ctx context.Context, r io.Reader) (ImportStats, error) {
	return s.ImportExportOpts(ctx, r, false)
}

// ImportExportOpts is ImportExport with control over the conflict policy:
// siteWins=true skips the local-edits-win journal replay (the site's values are
// taken verbatim) — used by a "site wins" pull.
func (s *EventService) ImportExportOpts(ctx context.Context, r io.Reader, siteWins bool) (ImportStats, error) {
	export, err := ParseEventExport(r)
	if err != nil {
		return ImportStats{}, err
	}

	store, err := s.catalog.OpenOrCreate(export.Event.ID)
	if err != nil {
		return ImportStats{}, err
	}

	return NewEventImporter(store).WithSkipLocalReplay(siteWins).Import(ctx, export)
}

// List scans the event catalog and reads each event's header row.
func (s *EventService) List(ctx context.Context) ([]EventInfo, error) {
	ids, err := s.catalog.ListEventIDs()
	if err != nil {
		return nil, err
	}

	infos := make([]EventInfo, 0, len(ids))
	for _, id := range ids {
		store, err := s.catalog.Open(id)
		if err != nil {
			s.logger.Printf("skip event file %s.chrono: %v", id, err)
			continue
		}
		event, err := FirstEvent(ctx, store)
		if err != nil {
			s.logger.Printf("skip event file %s.chrono: %v", id, err)
			continue
		}
		raceCount, memberCount, err := store.CountRacesAndMembers(ctx)
		if err != nil {
			s.logger.Printf("skip event file %s.chrono: %v", id, err)
			continue
		}
		infos = append(infos, EventInfo{
			ID: event.ID, Name: event.Name, Date: event.Date, Timezone: event.Timezone, File: id + ".chrono",
			RaceCount: raceCount, MemberCount: memberCount, UseRaceDateForAge: event.UseRaceDateForAge,
		})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Date > infos[j].Date })
	return infos, nil
}

// FirstEvent reads the single event header row from a store (one .chrono file
// holds exactly one event). Used by the event list and the public LAN server.
func FirstEvent(ctx context.Context, store *sqlite.Store) (domain.Event, error) {
	return store.FirstEvent(ctx)
}

// Close releases all open event databases.
func (s *EventService) Close() {
	if err := s.catalog.Close(); err != nil {
		s.logger.Printf("close event catalog: %v", err)
	}
}
