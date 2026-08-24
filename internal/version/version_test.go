package version

import (
	"testing"

	timing "gitlab.com/fightmaster1/timing-core"
)

// Get() must mirror the package vars, which the linker overwrites via -ldflags.
func TestGetReflectsVars(t *testing.T) {
	got := Get()
	if got.Version != Semver || got.Build != Build || got.Commit != Commit || got.Date != Date {
		t.Fatalf("Get() = %+v, want it to mirror the package vars", got)
	}
	if got.TimingCoreVersion != timing.ModuleVersion || got.MatcherVersion != timing.MatcherVersion ||
		got.MemberTimeVersion != timing.MemberTimeVersion ||
		got.OutcomeVersion != timing.ResultOutcomeVersion || got.RankingVersion != timing.RankingVersion ||
		got.ImpactVersion != timing.ImpactVersion {
		t.Fatalf("Get() timing-core identity = %+v, want published core constants", got)
	}
	if got.EventExportSchemaVersion != 3 || got.SyncPushSchemaVersion != 3 || got.ChangeFeedSchemaVersion != 1 {
		t.Fatalf("Get() transport contract identity = %+v", got)
	}
	t.Logf("version=%s build=%s commit=%s date=%q", got.Version, got.Build, got.Commit, got.Date)
}
