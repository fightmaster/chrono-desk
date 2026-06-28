package version

import "testing"

// Get() must mirror the package vars, which the linker overwrites via -ldflags.
func TestGetReflectsVars(t *testing.T) {
	got := Get()
	if got.Version != Semver || got.Build != Build || got.Commit != Commit || got.Date != Date {
		t.Fatalf("Get() = %+v, want it to mirror the package vars", got)
	}
	t.Logf("version=%s build=%s commit=%s date=%q", got.Version, got.Build, got.Commit, got.Date)
}
