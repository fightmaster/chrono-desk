package service

import (
	"context"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/processor"
	"gitlab.com/fightmaster1/chrono-desk/internal/ranking"
)

// ProtocolRow is the JSON shape of one ranked protocol line.
type ProtocolRow struct {
	MemberID      string  `json:"member_id"`
	Number        *int64  `json:"number"`
	FirstName     string  `json:"first_name"`
	LastName      string  `json:"last_name"`
	Gender        *string `json:"gender"`
	DOB           *string `json:"dob"`
	Team          *string `json:"team"`
	City          *string `json:"city"`
	CategoryID    *string `json:"category_id"`
	CategoryName  *string `json:"category_name"`
	Status        string  `json:"status"`
	Place         *int    `json:"place"`
	GenderPlace   *int    `json:"gender_place"`
	CategoryPlace *int    `json:"category_place"`
	CleanTimeMs   *int64  `json:"clean_time_ms"`
	CleanTime     *string `json:"clean_time"`

	// TimeLimited races only.
	LastCheckpointName *string `json:"last_checkpoint_name,omitempty"`
	ElapsedMs          *int64  `json:"elapsed_ms,omitempty"`
}

// ProtocolResponse bundles the race header with its ranked rows.
type ProtocolResponse struct {
	RaceID   string        `json:"race_id"`
	RaceName string        `json:"race_name"`
	Format   string        `json:"format"`
	Rows     []ProtocolRow `json:"rows"`
}

// BuildProtocol loads the race, ranks its members in memory and renders the
// JSON rows.
func BuildProtocol(ctx context.Context, store *sqlite.Store, raceID string) (ProtocolResponse, error) {
	race, err := store.GetRace(ctx, raceID)
	if err != nil {
		return ProtocolResponse{}, err
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
		if lastPasses, err = store.LastPassesInWindow(ctx, race, members); err != nil {
			return ProtocolResponse{}, err
		}
	}

	ranked := ranking.Protocol(race, members, lastPasses)

	resp := ProtocolResponse{
		RaceID:   race.ID,
		RaceName: race.Name,
		Format:   string(race.Format),
		Rows:     make([]ProtocolRow, 0, len(ranked)),
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
			City:          r.Member.City,
			CategoryID:    r.Member.CategoryID,
			Status:        r.Status,
			Place:         r.Place,
			GenderPlace:   r.GenderPlace,
			CategoryPlace: r.CategoryPlace,
			CleanTimeMs:   r.CleanTimeMs,
		}
		if r.CleanTimeMs != nil {
			formatted := processor.FormatCleanTime(0, *r.CleanTimeMs)
			row.CleanTime = &formatted
		}
		// TimeLimited: run5's compat shim renders the in-window elapsed as
		// the member's clean time.
		if r.ElapsedMs != nil {
			row.ElapsedMs = r.ElapsedMs
			row.LastCheckpointName = r.LastCheckpointName
			formatted := processor.FormatCleanTime(0, *r.ElapsedMs)
			row.CleanTime = &formatted
		}
		if r.Member.CategoryID != nil {
			if cat, ok := categories[*r.Member.CategoryID]; ok {
				name := cat.Name
				row.CategoryName = &name
			}
		}
		resp.Rows = append(resp.Rows, row)
	}
	return resp, nil
}
