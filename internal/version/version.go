// Package version carries the build identity, injected at build time via
// -ldflags -X (see the Makefile and .github/workflows/build.yml). The defaults
// below are what an un-stamped `go run`/`go test` build reports.
package version

import timing "gitlab.com/fightmaster1/timing-core"

var (
	Semver = "dev"  // semantic version, from the VERSION file
	Build  = "0"    // monotonic build number = git commit count
	Commit = "none" // full Git source revision
	Date   = ""     // source commit timestamp, RFC3339
)

const (
	EventExportSchemaVersion = 3
	SyncPushSchemaVersion    = 3
	ChangeFeedSchemaVersion  = 1
)

// Info is the JSON shape served by GET /api/version and shown in the UI.
type Info struct {
	Version           string `json:"version"`
	Build             string `json:"build"`
	Commit            string `json:"commit"`
	Date              string `json:"date"`
	TimingCoreVersion string `json:"timing_core_version"`
	MatcherVersion    string `json:"matcher_version"`
	MemberTimeVersion string `json:"member_time_version"`
	OutcomeVersion    string `json:"outcome_version"`
	RankingVersion    string `json:"ranking_version"`
	ImpactVersion     string `json:"impact_version"`

	EventExportSchemaVersion int `json:"event_export_schema_version"`
	SyncPushSchemaVersion    int `json:"sync_push_schema_version"`
	ChangeFeedSchemaVersion  int `json:"change_feed_schema_version"`
}

// Get returns the current build identity.
func Get() Info {
	return Info{
		Version: Semver, Build: Build, Commit: Commit, Date: Date,
		TimingCoreVersion:        timing.ModuleVersion,
		MatcherVersion:           timing.MatcherVersion,
		MemberTimeVersion:        timing.MemberTimeVersion,
		OutcomeVersion:           timing.ResultOutcomeVersion,
		RankingVersion:           timing.RankingVersion,
		ImpactVersion:            timing.ImpactVersion,
		EventExportSchemaVersion: EventExportSchemaVersion,
		SyncPushSchemaVersion:    SyncPushSchemaVersion,
		ChangeFeedSchemaVersion:  ChangeFeedSchemaVersion,
	}
}
