package processor

import (
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

func TestSelectCheckpointFromCorePreservesDesktopTypesAndEligibility(t *testing.T) {
	start := int64(1_000)
	lastSort := int64(1)
	lastTime := int64(5_000)
	offset := int64(10)
	member := Member{ID: "member", RaceID: "race", StartTimeMs: &start}
	checkpoints := []Checkpoint{
		{ID: "passed", Sort: 2, Type: domain.CheckpointMid},
		{ID: "not-active", Sort: 3, Type: domain.CheckpointFinish, SinceOffsetSeconds: &offset},
		{ID: "finish", Sort: 4, Type: domain.CheckpointFinish},
	}
	logEntry := domain.RfidLog{ID: "read", EventID: "event", TimeMs: 9_000, Number: 42, Board: "finish"}

	selected, ok := selectCheckpoint(logEntry, member, LastResult{Sort: &lastSort, TimeMs: &lastTime}, map[string]bool{"passed": true}, checkpoints)
	if !ok || selected.ID != "finish" || selected.Type != domain.CheckpointFinish {
		t.Fatalf("selected=%+v ok=%t, want finish", selected, ok)
	}
}
