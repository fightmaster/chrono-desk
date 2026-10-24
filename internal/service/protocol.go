package service

import (
	"context"
	"fmt"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/processor"
	"gitlab.com/fightmaster1/chrono-desk/internal/ranking"
)

// ProtocolRow is the JSON shape of one ranked protocol line.
type ProtocolRow struct {
	GunTimeMs       *int64  `json:"gun_time_ms"`
	ChipTimeMs      *int64  `json:"chip_time_ms"`
	ChipStartSource string  `json:"chip_start_source"`
	MemberID        string  `json:"member_id"`
	Number          *int64  `json:"number"`
	FirstName       string  `json:"first_name"`
	LastName        string  `json:"last_name"`
	Gender          *string `json:"gender"`
	DOB             *string `json:"dob"`
	Team            *string `json:"team"`
	City            *string `json:"city"`
	CategoryID      *string `json:"category_id"`
	CategoryName    *string `json:"category_name"`
	Status          string  `json:"status"`
	Place           *int    `json:"place"`
	GenderPlace     *int    `json:"gender_place"`
	CategoryPlace   *int    `json:"category_place"`
	CleanTimeMs     *int64  `json:"clean_time_ms"`
	CleanTime       *string `json:"clean_time"`
	Club            *string `json:"club"`

	// TimeLimited races only.
	LastCheckpointName *string `json:"last_checkpoint_name,omitempty"`
	ElapsedMs          *int64  `json:"elapsed_ms,omitempty"`
}

// ProtocolCounts retain the existing protocol API tallies. Started is the legacy
// denominator (registrations minus explicit DNS), including DNF/DSQ. It is not
// evidence that a person physically started or is still expected at finish.
// The operational header and on-course roster use Progress instead.
type ProtocolCounts struct {
	Total    int `json:"total"`    // start list (all members of the race)
	Started  int `json:"started"`  // total − DNS
	Finished int `json:"finished"` // members with a place
	DNS      int `json:"dns"`
	DNF      int `json:"dnf"`
	DSQ      int `json:"dsq"`
}

// ProtocolResponse bundles the race header with its ranked rows.
type ProtocolResponse struct {
	TimingBasis string         `json:"timing_basis"`
	RaceID      string         `json:"race_id"`
	RaceName    string         `json:"race_name"`
	Format      string         `json:"format"`
	Counts      ProtocolCounts `json:"counts"`
	Rows        []ProtocolRow  `json:"rows"`
	Progress    RaceProgress   `json:"progress"`
}

// BuildProtocol loads the race, ranks its members in memory and renders the
// JSON rows.
func BuildProtocol(ctx context.Context, store ProtocolStore, raceID string) (ProtocolResponse, error) {
	race, err := store.GetRace(ctx, raceID)
	if err != nil {
		return ProtocolResponse{}, err
	}
	// Fail closed on formats we don't rank yet (e.g. Run5Stopwatch): ranking
	// would silently fall back to FixedDistance and produce a plausible but
	// wrong protocol. Refuse loudly instead of mis-ranking.
	switch race.Format {
	case domain.FormatFixedDistance, domain.FormatTimeLimited:
	default:
		return ProtocolResponse{}, fmt.Errorf("формат гонки %q пока не поддерживается — протокол не построен", race.Format)
	}
	members, err := store.ListMembersByRace(ctx, raceID)
	if err != nil {
		return ProtocolResponse{}, err
	}
	categories, err := store.ListCategories(ctx)
	if err != nil {
		return ProtocolResponse{}, err
	}

	var lastPasses map[string]ranking.LastPass
	if race.Format == domain.FormatTimeLimited {
		if lastPasses, err = store.LastPassesInWindow(ctx, race); err != nil {
			return ProtocolResponse{}, err
		}
	}

	reserved, err := store.ListReservedMemberIDs(ctx, race.EventID)
	if err != nil {
		return ProtocolResponse{}, err
	}
	ranked := ranking.Protocol(race, members, lastPasses)
	progress := buildRaceProgress(race, members, ranked, categories, reserved, time.Now().UnixMilli())

	counts := ProtocolCounts{Total: len(members)}
	for _, m := range members {
		switch m.Status {
		case domain.StatusDNS:
			counts.DNS++
		case domain.StatusDNF:
			counts.DNF++
		case domain.StatusDSQ:
			counts.DSQ++
		}
	}
	counts.Started = counts.Total - counts.DNS

	resp := ProtocolResponse{
		Progress:    progress,
		RaceID:      race.ID,
		RaceName:    race.Name,
		TimingBasis: string(race.TimingBasis),
		Format:      string(race.Format),
		Counts:      counts,
		Rows:        make([]ProtocolRow, 0, len(ranked)),
	}
	for _, r := range ranked {
		row := ProtocolRow{
			MemberID:      r.Member.ID,
			Number:        r.Member.Number,
			FirstName:     r.Member.FirstName,
			LastName:      r.Member.LastName,
			Gender:        r.Member.Gender,
			DOB:           r.Member.DOB,
			Team:          r.Member.Team,
			Club:          r.Member.Club,
			City:          r.Member.City,
			CategoryID:    r.Member.CategoryID,
			Status:        r.Status,
			Place:         r.Place,
			GenderPlace:   r.GenderPlace,
			CategoryPlace: r.CategoryPlace,
			CleanTimeMs:   r.CleanTimeMs,
			GunTimeMs:     r.GunTimeMs, ChipTimeMs: r.ChipTimeMs, ChipStartSource: r.ChipStartSource,
		}
		if r.CleanTimeMs != nil {
			formatted := processor.FormatCleanTime(0, *r.CleanTimeMs)
			row.CleanTime = &formatted
		}
		// TimeLimited: run5's compat shim renders the in-window elapsed as
		// the member's clean time — including its ms, so the «Отставание» gap
		// (computed off CleanTimeMs) anchors on the winner like FixedDistance.
		if r.ElapsedMs != nil {
			row.ElapsedMs = r.ElapsedMs
			row.LastCheckpointName = r.LastCheckpointName
			row.CleanTimeMs = r.ElapsedMs
			formatted := processor.FormatCleanTime(0, *r.ElapsedMs)
			row.CleanTime = &formatted
		}
		if r.Member.CategoryID != nil {
			if cat, ok := categories[*r.Member.CategoryID]; ok {
				name := cat.Name
				row.CategoryName = &name
			}
		}
		if row.Place != nil {
			resp.Counts.Finished++
		}
		resp.Rows = append(resp.Rows, row)
	}
	return resp, nil
}
