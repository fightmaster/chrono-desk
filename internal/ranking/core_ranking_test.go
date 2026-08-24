package ranking

import (
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

func TestRankInputFromRowPreservesMemberDimensions(t *testing.T) {
	gender := "female"
	category := "open"
	rank := int64(-42)
	row := &rankRow{
		member: domain.Member{ID: "member", Gender: &gender, CategoryID: &category},
		status: "ok", rankPrimary: &rank,
	}

	input := rankInputFromRow(row)
	if input.MemberID != "member" || input.TieBreakKey != "1:member" || input.Gender == nil || *input.Gender != gender || input.CategoryID == nil || *input.CategoryID != category {
		t.Fatalf("input=%+v", input)
	}
	if input.RankPrimary == nil || *input.RankPrimary != -42 {
		t.Fatalf("rank=%v", input.RankPrimary)
	}
}
