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
		got.OutcomeVersion != timing.ResultOutcomeVersion || got.RankingVersion != timing.RankingVersion {
		t.Fatalf("Get() timing-core identity = %+v, want published core constants", got)
	}
	t.Logf("version=%s build=%s commit=%s date=%q", got.Version, got.Build, got.Commit, got.Date)
}
