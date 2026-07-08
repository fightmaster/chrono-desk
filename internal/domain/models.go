// Package domain holds the core entities mirroring the run5 data model.
// Field semantics follow docs/event-export-format.md; all instants are unix
// milliseconds unless a field is documented otherwise.
package domain

// RaceFormat mirrors run5's races.format enum.
type RaceFormat string

const (
	FormatFixedDistance RaceFormat = "FixedDistance"
	FormatTimeLimited   RaceFormat = "TimeLimited"
	FormatRun5Stopwatch RaceFormat = "Run5Stopwatch"
)

// CheckpointType mirrors run5's checkpoints.type.
type CheckpointType int

const (
	CheckpointStart  CheckpointType = 1
	CheckpointMid    CheckpointType = 2
	CheckpointFinish CheckpointType = 3
)

// MemberStatus mirrors run5's MemberStatus enum; zero value means a regular
// (ok) member.
type MemberStatus int

const (
	StatusOK  MemberStatus = 0
	StatusDNS MemberStatus = 1
	StatusDNF MemberStatus = 2
	StatusDSQ MemberStatus = 3
)

type Event struct {
	ID                string
	Name              string
	Slug              string
	Date              string // ISO date (YYYY-MM-DD)
	Timezone          string // IANA zone of the venue (from the export)
	UseRaceDateForAge bool
}

type Lap struct {
	ID          string
	Name        string
	Slug        string
	Description string
}

type Race struct {
	ID                          string
	EventID                     string
	Name                        string
	Date                        string // ISO date
	StartedAtMs                 *int64
	LapID                       *string
	Format                      RaceFormat
	TimeLimitSeconds            *int64
	CategoryExcludesTopByGender bool
}

type Category struct {
	ID     string
	Name   string
	Min    *int
	Max    *int
	Gender *string
}

type Checkpoint struct {
	ID                    string
	EventID               string
	RaceID                string
	Name                  string
	Type                  CheckpointType
	Sort                  int64
	Board                 string
	SinceMs               *int64
	SinceOffsetSeconds    *int64
	SleepAfterPrevSeconds *int64
}

type Member struct {
	ID         string
	EventID    string
	RaceID     string
	CategoryID *string
	Number     *int64 // bib
	EPC        *string
	RFID       *string
	FirstName  string
	LastName   string
	Gender     *string
	DOB        *string // ISO date
	City       *string
	Team       *string
	Status     MemberStatus

	// Derived locally on recount; export values are informational only.
	StartTimeMs  *int64
	FinishTimeMs *int64
	CleanTime    *string // HH:MM:SS.mmm
}

// RfidLog is a raw reader event. ID is the cross-system idempotency key:
// md5(board + epc + timeMillis + ant) — see docs/architecture.md, decision 4.
type RfidLog struct {
	ID         string
	EventID    string
	Status     int
	Number     int64
	TimeMs     int64
	Ant        int
	EPC        string
	RSSI       int
	Board      string
	DisabledAt *int64 // unix ms; non-nil logs are skipped on recount (run5 ADR-0007)
}

// Result is one derived checkpoint pass; at most one per rfid log.
type Result struct {
	ID           int64 // local autoincrement
	EventID      string
	RaceID       string
	MemberID     string
	CheckpointID string
	RfidLogID    *string // nil for manual results
	TimeMs       int64
	Number       *int64
}
