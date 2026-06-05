// Package ranking computes protocol order and places. It is the Go port of
// run5's results system (docs/ranking.md): stage 1 materializes per-member
// rank rows by race format, stage 2 sorts them and assigns overall, gender
// and category places. Everything is computed in memory at read time — there
// is no materialized table to go stale.
package ranking

import (
	"sort"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

// Row is one protocol line after ranking.
type Row struct {
	Member        domain.Member
	Status        string // "ok" | "dns" | "dnf" | "dq"
	CleanTimeMs   *int64
	Place         *int // overall, dense over 'ok' rows
	GenderPlace   *int
	CategoryPlace *int
}

// rankRow mirrors run5's member_results semantics.
type rankRow struct {
	member        domain.Member
	status        string
	rankPrimary   *int64
	rankSecondary *int64
	rankTertiary  *int64
	cleanTimeMs   *int64
}

func statusString(s domain.MemberStatus) string {
	switch s {
	case domain.StatusDNS:
		return "dns"
	case domain.StatusDNF:
		return "dnf"
	case domain.StatusDSQ:
		return "dq"
	default:
		return "ok"
	}
}

// materializeFixedDistance ports FixedDistanceFormat::materialize: a status
// code wins over times; otherwise the member needs start+finish+clean to
// enter the protocol; rank_primary = -cleanTimeMs.
func materializeFixedDistance(m domain.Member) *rankRow {
	if m.Status != domain.StatusOK {
		return &rankRow{member: m, status: statusString(m.Status)}
	}
	if m.StartTimeMs == nil || m.FinishTimeMs == nil || m.CleanTime == nil {
		return nil // not finished yet — absent from the protocol
	}
	clean := *m.FinishTimeMs - *m.StartTimeMs
	if clean < 0 {
		return nil
	}
	rank := -clean
	return &rankRow{member: m, status: "ok", rankPrimary: &rank, cleanTimeMs: &clean}
}

// Protocol builds the ranked protocol for a FixedDistance race.
// Ordering ports run5's rankRows: 'ok' rows first, then rank fields
// descending with nulls last; the sort is stable (no explicit tie-break in
// run5 — input order is preserved). Places are dense over 'ok' rows.
func Protocol(race domain.Race, members []domain.Member) []Row {
	rows := make([]*rankRow, 0, len(members))
	for _, m := range members {
		if r := materializeFixedDistance(m); r != nil {
			rows = append(rows, r)
		}
	}

	sort.SliceStable(rows, func(i, j int) bool {
		return lessRank(rows[i], rows[j])
	})

	out := make([]Row, 0, len(rows))
	place := 0
	for _, r := range rows {
		row := Row{Member: r.member, Status: r.status, CleanTimeMs: r.cleanTimeMs}
		if r.status == "ok" {
			place++
			p := place
			row.Place = &p
		}
		out = append(out, row)
	}

	assignGenderPlaces(out)
	assignCategoryPlaces(out, race.CategoryExcludesTopByGender)
	return out
}

// lessRank: ok ahead of non-ok; then rank_primary/secondary/tertiary DESC,
// nil sorting last within each level.
func lessRank(a, b *rankRow) bool {
	aOK, bOK := a.status == "ok", b.status == "ok"
	if aOK != bOK {
		return aOK
	}
	if c := compareDescNullsLast(a.rankPrimary, b.rankPrimary); c != 0 {
		return c < 0
	}
	if c := compareDescNullsLast(a.rankSecondary, b.rankSecondary); c != 0 {
		return c < 0
	}
	if c := compareDescNullsLast(a.rankTertiary, b.rankTertiary); c != 0 {
		return c < 0
	}
	return false // stable sort keeps input order
}

// compareDescNullsLast returns <0 when a ranks ahead of b under DESC order
// with nulls last.
func compareDescNullsLast(a, b *int64) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	case *a > *b:
		return -1
	case *a < *b:
		return 1
	default:
		return 0
	}
}

func assignGenderPlaces(rows []Row) {
	counters := map[string]int{}
	for i := range rows {
		if rows[i].Place == nil || rows[i].Member.Gender == nil {
			continue
		}
		g := *rows[i].Member.Gender
		counters[g]++
		p := counters[g]
		rows[i].GenderPlace = &p
	}
}

// topPerGender is run5's hard-coded exclusion depth for
// ExcludeTopByGenderCategoryRankingStrategy.
const topPerGender = 3

func assignCategoryPlaces(rows []Row, excludeTopByGender bool) {
	excluded := map[string]bool{} // member id → excluded from category standings
	if excludeTopByGender {
		seen := map[string]int{}
		for _, r := range rows {
			if r.Place == nil || r.Member.Gender == nil {
				continue
			}
			g := *r.Member.Gender
			if seen[g] < topPerGender {
				seen[g]++
				excluded[r.Member.ID] = true
			}
		}
	}

	counters := map[string]int{}
	for i := range rows {
		r := &rows[i]
		if r.Place == nil || r.Member.CategoryID == nil || *r.Member.CategoryID == "" {
			continue
		}
		if excluded[r.Member.ID] {
			continue
		}
		c := *r.Member.CategoryID
		counters[c]++
		p := counters[c]
		r.CategoryPlace = &p
	}
}
