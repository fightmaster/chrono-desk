package sqlite

import (
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/processor"
	timing "gitlab.com/fightmaster1/timing-core"
)

func TestPlanMemberTimesMapsDesktopDomainToSharedProjection(t *testing.T) {
	raceStart := int64(1_000)
	member := processor.Member{
		ID: "member", RaceID: "race", RaceStartedAtMs: &raceStart,
	}
	checkpoint := processor.Checkpoint{ID: "finish", Type: domain.CheckpointFinish}

	plan := planMemberTimes(member, checkpoint, 61_007)
	if plan.StartWrite != timing.StartWriteIfNull || plan.StartTimeMs == nil || *plan.StartTimeMs != raceStart {
		t.Fatalf("start plan=%+v, want race-start backfill", plan)
	}
	if plan.FinishTimeMs == nil || *plan.FinishTimeMs != 61_007 {
		t.Fatalf("finish plan=%+v, want accepted finish", plan)
	}
	if plan.CleanTime == nil || *plan.CleanTime != "00:01:00.007" {
		t.Fatalf("clean time=%v, want shared legacy format", plan.CleanTime)
	}
}

func TestPlanMemberTimesMapsExistingDesktopFinish(t *testing.T) {
	start := int64(1_000)
	finish := int64(5_000)
	member := processor.Member{
		ID: "member", RaceID: "race", StartTimeMs: &start, FinishTimeMs: &finish,
	}
	checkpoint := processor.Checkpoint{ID: "finish", Type: domain.CheckpointFinish}

	plan := planMemberTimes(member, checkpoint, 9_000)
	if plan.FinishTimeMs != nil || plan.EffectiveFinishTimeMs == nil || *plan.EffectiveFinishTimeMs != finish {
		t.Fatalf("finish plan=%+v, want existing finish preserved", plan)
	}
}
