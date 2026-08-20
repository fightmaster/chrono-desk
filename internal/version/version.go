// Package version carries the build identity, injected at build time via
// -ldflags -X (see the Makefile and .github/workflows/build.yml). The defaults
// below are what an un-stamped `go run`/`go test` build reports.
package version

import timing "gitlab.com/fightmaster1/timing-core"

var (
	Semver = "dev"  // semantic version, from the VERSION file
	Build  = "0"    // monotonic build number = git commit count
	Commit = "none" // short git SHA
	Date   = ""     // build timestamp, UTC RFC3339
)

// Info is the JSON shape served by GET /api/version and shown in the UI.
type Info struct {
	Version           string `json:"version"`
	Build             string `json:"build"`
	Commit            string `json:"commit"`
	Date              string `json:"date"`
	TimingCoreVersion string `json:"timing_core_version"`
	MatcherVersion    string `json:"matcher_version"`
	OutcomeVersion    string `json:"outcome_version"`
	RankingVersion    string `json:"ranking_version"`
}

// Get returns the current build identity.
func Get() Info {
	return Info{
		Version: Semver, Build: Build, Commit: Commit, Date: Date,
		TimingCoreVersion: timing.ModuleVersion,
		MatcherVersion:    timing.MatcherVersion,
		OutcomeVersion:    timing.ResultOutcomeVersion,
		RankingVersion:    timing.RankingVersion,
	}
}
