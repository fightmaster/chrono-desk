package service

import (
	"context"
	"fmt"
	"sort"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	timing "gitlab.com/fightmaster1/timing-core"
)

type memberCategoryResolver interface {
	GetRace(ctx context.Context, raceID string) (domain.Race, error)
	GetEvent(ctx context.Context, id string) (domain.Event, error)
	ListRaceCategories(ctx context.Context, raceID string) ([]domain.Category, error)
}

func resolveCategoryIDForMember(ctx context.Context, store memberCategoryResolver, member domain.Member) (*string, error) {
	if member.Gender == nil || *member.Gender == "" || member.DOB == nil || *member.DOB == "" {
		return nil, nil
	}

	race, err := store.GetRace(ctx, member.RaceID)
	if err != nil {
		return nil, err
	}
	event, err := store.GetEvent(ctx, member.EventID)
	if err != nil {
		return nil, err
	}
	categories, err := store.ListRaceCategories(ctx, member.RaceID)
	if err != nil {
		return nil, err
	}

	return resolveCategoryID(race, event, categories, *member.Gender, *member.DOB)
}

func resolveCategoryID(race domain.Race, event domain.Event, categories []domain.Category, gender, dob string) (*string, error) {
	birthDate, err := time.Parse("2006-01-02", dob)
	if err != nil {
		return nil, fmt.Errorf("parse dob %q: %w", dob, err)
	}
	raceDate, err := parseRaceDate(race.Date)
	if err != nil {
		return nil, fmt.Errorf("parse race date %q: %w", race.Date, err)
	}

	age := timing.AgeOnRaceDate(birthDate, raceDate, event.UseRaceDateForAge)
	sort.Slice(categories, func(left, right int) bool { return categories[left].ID < categories[right].ID })

	for _, category := range categories {
		if category.Gender == nil || *category.Gender != gender {
			continue
		}
		if category.Min != nil && age < *category.Min {
			continue
		}
		if category.Max != nil && age > *category.Max {
			continue
		}

		id := category.ID
		return &id, nil
	}

	return nil, nil
}

func parseRaceDate(value string) (time.Time, error) {
	if len(value) < len("2006-01-02") {
		return time.Time{}, fmt.Errorf("too short")
	}
	return time.Parse("2006-01-02", value[:10])
}
