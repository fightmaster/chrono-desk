package service

import (
	"sort"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/ranking"
)

// RaceProgress is an operational expectation, independent of official ranking.
// In a mass start, registration plus the configured start does not prove that a
// participant physically started. Silence must never automatically become DNS.
type RaceProgress struct {
	Counts   RaceProgressCounts `json:"counts"`
	OnCourse []OnCourseRow      `json:"on_course"`
}

type RaceProgressCounts struct {
	Total       int `json:"total"` // non-reserve registrations, all states below
	Finished    int `json:"finished"`
	OnCourse    int `json:"on_course"`
	NotStarted  int `json:"not_started"` // future or unknown effective start, not DNS
	DNS         int `json:"dns"`
	DNF         int `json:"dnf"`
	DSQ         int `json:"dsq"`
	NeedsReview int `json:"needs_review"`
	Reserve     int `json:"reserve"` // outside the active start list
}

type OnCourseRow struct {
	MemberID     string  `json:"member_id"`
	Number       *int64  `json:"number"`
	FirstName    string  `json:"first_name"`
	LastName     string  `json:"last_name"`
	Gender       *string `json:"gender"`
	CategoryID   *string `json:"category_id"`
	CategoryName *string `json:"category_name"`
	StartTimeMs  int64   `json:"start_time_ms"`
}

func buildRaceProgress(race domain.Race, members []domain.Member, ranked []ranking.Row, categories map[string]domain.Category, reserved map[string]bool, nowMs int64) RaceProgress {
	progress := RaceProgress{OnCourse: make([]OnCourseRow, 0)}
	classified := make(map[string]bool, len(ranked))
	for _, row := range ranked {
		if row.Place != nil {
			classified[row.Member.ID] = true
		}
	}
	for _, member := range members {
		if reserved[member.ID] {
			progress.Counts.Reserve++
			continue
		}
		progress.Counts.Total++
		switch member.Status {
		case domain.StatusDNS:
			progress.Counts.DNS++
			continue
		case domain.StatusDNF:
			progress.Counts.DNF++
			continue
		case domain.StatusDSQ:
			progress.Counts.DSQ++
			continue
		case domain.StatusOK:
		default:
			progress.Counts.NeedsReview++
			continue
		}
		if member.FinishTimeMs != nil {
			if classified[member.ID] {
				progress.Counts.Finished++
			} else {
				progress.Counts.NeedsReview++
			}
			continue
		}
		start := member.StartTimeMs
		if start == nil {
			start = race.StartedAtMs
		}
		if start == nil || *start <= 0 || *start > nowMs {
			progress.Counts.NotStarted++
			continue
		}
		if race.Format == domain.FormatTimeLimited {
			if race.TimeLimitSeconds == nil || *race.TimeLimitSeconds <= 0 {
				progress.Counts.NeedsReview++
				continue
			}
			// Subtract before division to avoid overflow from a malformed limit.
			if (nowMs-*start)/1000 >= *race.TimeLimitSeconds {
				if classified[member.ID] {
					progress.Counts.Finished++
				} else {
					progress.Counts.NeedsReview++
				}
				continue
			}
		}
		row := OnCourseRow{MemberID: member.ID, Number: member.Number,
			FirstName: member.FirstName, LastName: member.LastName, Gender: member.Gender,
			CategoryID: member.CategoryID, StartTimeMs: *start}
		if member.CategoryID != nil {
			if category, found := categories[*member.CategoryID]; found {
				name := category.Name
				row.CategoryName = &name
			}
		}
		progress.OnCourse = append(progress.OnCourse, row)
	}
	progress.Counts.OnCourse = len(progress.OnCourse)
	sort.SliceStable(progress.OnCourse, func(i, j int) bool {
		a, b := progress.OnCourse[i], progress.OnCourse[j]
		if a.Number != nil && b.Number != nil && *a.Number != *b.Number {
			return *a.Number < *b.Number
		}
		if (a.Number == nil) != (b.Number == nil) {
			return a.Number != nil
		}
		if a.LastName != b.LastName {
			return a.LastName < b.LastName
		}
		if a.FirstName != b.FirstName {
			return a.FirstName < b.FirstName
		}
		return a.MemberID < b.MemberID
	})
	return progress
}
