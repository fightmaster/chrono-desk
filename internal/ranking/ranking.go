// Package ranking computes protocol order and places. It is the Go port of
// run5's results system (docs/ranking.md): stage 1 materializes per-member
// rank rows by race format, stage 2 sorts them and assigns overall, gender
// and category places. Everything is computed in memory at read time — there
// is no materialized table to go stale.
package ranking

import (
	"sort"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	timing "gitlab.com/fightmaster1/timing-core"
)

// Row is one protocol line after ranking.
type Row struct {
	Member        domain.Member
	Status        string // "ok" | "dns" | "dnf" | "dq"
	CleanTimeMs   *int64
	Place         *int // overall, dense over 'ok' rows
	GenderPlace   *int
	CategoryPlace *int

	// TimeLimited extras (run5's TimeLimitedResultPayload analog).
	LastCheckpointName *string
	LastPassAtMs       *int64
	ElapsedMs          *int64
}

// LastPass is a member's final result inside the time-limited window —
// the row picked by ORDER BY time_ms DESC, id DESC (mirrors rfid-sync's
// LoadLastPassInWindow / run5's TimeLimitedFormat).
type LastPass struct {
	TimeMs         int64
	CheckpointSort *int64
	CheckpointName *string
}

// rankRow mirrors run5's member_results semantics.
type rankRow struct {
	member        domain.Member
	status        string
	rankPrimary   *int64
	rankSecondary *int64
	rankTertiary  *int64
	cleanTimeMs   *int64

	// TimeLimited payload analog.
	elapsedMs          *int64
	lastPassAtMs       *int64
	lastCheckpointName *string
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
	outcome, ok, err := timing.FixedDistanceOutcome(m.ID, m.RaceID, m.StartTimeMs, m.FinishTimeMs)
	if err != nil || !ok {
		return nil
	}
	return rankRowFromCore(m, outcome)
}

// materializeTimeLimited ports TimeLimitedFormat::materialize (reference Go
// implementation: rfid-sync's TimeLimitedMemberResult): the member's last
// pass inside [start, start + time_limit] wins; rank_primary is the
// checkpoint sort (a later checkpoint wins, NOT negated); rank_secondary is
// -elapsedMs (within the same checkpoint a faster pass wins, clamped at 0).
func materializeTimeLimited(race domain.Race, m domain.Member, pass *LastPass) *rankRow {
	if m.Status != domain.StatusOK {
		return &rankRow{member: m, status: statusString(m.Status)}
	}
	if m.StartTimeMs == nil || race.TimeLimitSeconds == nil || *race.TimeLimitSeconds <= 0 || pass == nil {
		return nil
	}
	outcome, err := timing.TimeLimitedOutcome(
		m.ID,
		m.RaceID,
		timing.LastPass{
			TimeMs: pass.TimeMs, CheckpointSort: pass.CheckpointSort,
			CheckpointName: pass.CheckpointName,
		},
		*m.StartTimeMs,
	)
	if err != nil {
		return nil
	}
	return rankRowFromCore(m, outcome)
}

func rankRowFromCore(member domain.Member, outcome timing.ResultOutcome[string]) *rankRow {
	return &rankRow{
		member: member, status: outcome.Status,
		rankPrimary: outcome.RankPrimary, rankSecondary: outcome.RankSecondary,
		cleanTimeMs: outcome.CleanTimeMs, elapsedMs: outcome.ElapsedMs,
		lastPassAtMs:       outcome.LastPassAtMs,
		lastCheckpointName: outcome.LastCheckpointName,
	}
}

// Protocol builds the ranked protocol for a race. lastPasses (keyed by
// member id) is required for TimeLimited races and ignored otherwise.
// Ordering ports run5's rankRows: 'ok' rows first, then rank fields
// descending with nulls last; the sort is stable (no explicit tie-break in
// run5 — input order is preserved). Places are dense over 'ok' rows.
func Protocol(race domain.Race, members []domain.Member, lastPasses map[string]LastPass) []Row {
	rows := make([]*rankRow, 0, len(members))
	for _, m := range members {
		var r *rankRow
		switch race.Format {
		case domain.FormatTimeLimited:
			var pass *LastPass
			if p, ok := lastPasses[m.ID]; ok {
				pass = &p
			}
			r = materializeTimeLimited(race, m, pass)
		default: // FixedDistance (Run5Stopwatch pending)
			r = materializeFixedDistance(m)
		}
		if r != nil {
			rows = append(rows, r)
		}
	}

	sort.SliceStable(rows, func(i, j int) bool {
		return lessRank(rows[i], rows[j])
	})

	out := make([]Row, 0, len(rows))
	place := 0
	for _, r := range rows {
		row := Row{
			Member:             r.member,
			Status:             r.status,
			CleanTimeMs:        r.cleanTimeMs,
			ElapsedMs:          r.elapsedMs,
			LastPassAtMs:       r.lastPassAtMs,
			LastCheckpointName: r.lastCheckpointName,
		}
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
