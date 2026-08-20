package ranking

import (
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	timing "gitlab.com/fightmaster1/timing-core"
)

func TestRankRowFromCorePreservesDesktopMemberAndRankingFields(t *testing.T) {
	rank := int64(-42)
	clean := int64(42)
	member := domain.Member{ID: "member", RaceID: "race"}
	outcome := timing.ResultOutcome[string]{
		MemberID: member.ID, RaceID: member.RaceID,
		Format: timing.FormatFixedDistance, Status: timing.ResultStatusOK,
		RankPrimary: &rank, CleanTimeMs: &clean,
	}

	row := rankRowFromCore(member, outcome)
	if row.member.ID != member.ID || row.status != "ok" {
		t.Fatalf("row=%+v", row)
	}
	if row.rankPrimary == nil || *row.rankPrimary != -42 || row.cleanTimeMs == nil || *row.cleanTimeMs != 42 {
		t.Fatalf("rank/clean=%v/%v", row.rankPrimary, row.cleanTimeMs)
	}
}
