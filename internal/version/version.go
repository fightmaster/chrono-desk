// Package version carries the build identity, injected at build time via
// -ldflags -X (see the Makefile and .github/workflows/build.yml). The defaults
// below are what an un-stamped `go run`/`go test` build reports.
package version

var (
	Semver = "dev"  // semantic version, from the VERSION file
	Build  = "0"    // monotonic build number = git commit count
	Commit = "none" // short git SHA
	Date   = ""     // build timestamp, UTC RFC3339
)

// Info is the JSON shape served by GET /api/version and shown in the UI.
type Info struct {
	Version string `json:"version"`
	Build   string `json:"build"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

// Get returns the current build identity.
func Get() Info {
	return Info{Version: Semver, Build: Build, Commit: Commit, Date: Date}
}
