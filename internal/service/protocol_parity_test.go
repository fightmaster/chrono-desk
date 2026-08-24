package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	protocolranking "gitlab.com/fightmaster1/chrono-desk/internal/ranking"
	timing "gitlab.com/fightmaster1/timing-core"
)

const protocolParityFixtureSHA256 = "8423a970d3d5d9fa36f78275e011b815a41bf3ec42e407debbf9c7ed1bd0dd72"

func TestProtocolAdapterMatchesSharedParityFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/parity/protocol-parity-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != protocolParityFixtureSHA256 {
		t.Fatalf("fixture sha256 = %s, want %s", got, protocolParityFixtureSHA256)
	}
	var fixture struct {
		FixtureVersion int `json:"fixture_version"`
		Contracts      struct {
			Ranking string `json:"ranking"`
		} `json:"contracts"`
		Ranking struct {
			Exclude bool `json:"exclude_top_by_gender"`
			Inputs  []struct {
				MemberID    string  `json:"member_id"`
				Status      string  `json:"status"`
				Gender      *string `json:"gender"`
				CategoryID  *string `json:"category_id"`
				RankPrimary *int64  `json:"rank_primary"`
			} `json:"inputs"`
			Expected []struct {
				MemberID      string `json:"member_id"`
				Place         *int   `json:"place"`
				GenderPlace   *int   `json:"gender_place"`
				CategoryPlace *int   `json:"category_place"`
			} `json:"expected"`
		} `json:"ranking"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.FixtureVersion != 2 || fixture.Contracts.Ranking != timing.RankingVersion {
		t.Fatalf("unsupported fixture version/contracts")
	}

	race := domain.Race{ID: "race", Format: domain.FormatFixedDistance, CategoryExcludesTopByGender: fixture.Ranking.Exclude}
	members := make([]domain.Member, 0, len(fixture.Ranking.Inputs))
	for _, input := range fixture.Ranking.Inputs {
		member := domain.Member{
			ID: input.MemberID, RaceID: race.ID, Gender: input.Gender,
			CategoryID: input.CategoryID,
		}
		if input.Status == "dns" {
			member.Status = domain.StatusDNS
		} else if input.RankPrimary != nil {
			start := int64(0)
			finish := -*input.RankPrimary
			clean := "fixture"
			member.StartTimeMs, member.FinishTimeMs, member.CleanTime = &start, &finish, &clean
		}
		members = append(members, member)
	}

	rows := protocolranking.Protocol(race, members, nil)
	got := make([]struct {
		MemberID      string `json:"member_id"`
		Place         *int   `json:"place"`
		GenderPlace   *int   `json:"gender_place"`
		CategoryPlace *int   `json:"category_place"`
	}, 0, len(rows))
	for _, row := range rows {
		got = append(got, struct {
			MemberID      string `json:"member_id"`
			Place         *int   `json:"place"`
			GenderPlace   *int   `json:"gender_place"`
			CategoryPlace *int   `json:"category_place"`
		}{row.Member.ID, row.Place, row.GenderPlace, row.CategoryPlace})
	}
	if !reflect.DeepEqual(got, fixture.Ranking.Expected) {
		t.Fatalf("protocol ranking = %+v, want %+v", got, fixture.Ranking.Expected)
	}
}
