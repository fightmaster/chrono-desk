// Package packetissuance implements the transport-independent packet issuance
// contract shared by Chrono Desk's HTTPS adapter and SQLite repository.
package packetissuance

import "encoding/json"

const MaxSafeInteger int64 = 9007199254740991

type Person struct {
	ID        string `json:"id"`
	FirstName string `json:"firstName"`
	LastName  string `json:"lastName"`
	BirthDate string `json:"birthDate"`
	Gender    string `json:"gender"`
	Team      string `json:"team"`
	City      string `json:"city"`
}

type Registration struct {
	ID                string  `json:"id"`
	EventID           string  `json:"eventId"`
	RaceID            string  `json:"raceId"`
	Bib               string  `json:"bib"`
	EPC               string  `json:"epc"`
	Person            *Person `json:"person"`
	Reserve           bool    `json:"reserve"`
	Issued            bool    `json:"issued"`
	Status            string  `json:"status"`
	TransferredTo     *string `json:"transferredTo"`
	HasTimingEvidence bool    `json:"hasTimingEvidence"`
}

type Command struct {
	Type           string
	RegistrationID string
	Bib            string
	EventID        string
	RaceID         string
	Reason         string
	Value          *bool
	Fields         map[string]string
	Person         *Person
	ReturnSource   *bool
	IssuePacket    *bool
	TargetID       string
	OperationID    string
	Inputs         []string
	Keep           []string
	raw            json.RawMessage
}

func (c Command) MarshalJSON() ([]byte, error) { return append([]byte(nil), c.raw...), nil }

type Base struct {
	RegistrationID string   `json:"registrationId"`
	Heads          []string `json:"heads"`
}

type Change struct {
	RegistrationID string       `json:"registrationId"`
	Before         Registration `json:"before"`
	After          Registration `json:"after"`
}

type Operation struct {
	SchemaVersion    int64    `json:"schemaVersion"`
	OperationID      string   `json:"operationId"`
	ScopeID          string   `json:"scopeId"`
	BaselineID       string   `json:"baselineId"`
	OriginInstanceID string   `json:"originInstanceId"`
	OriginSequence   int64    `json:"originSequence"`
	CreatedAtMs      int64    `json:"createdAtMs"`
	ClaimedActor     string   `json:"claimedActor"`
	Command          Command  `json:"command"`
	Bases            []Base   `json:"bases"`
	Changes          []Change `json:"changes"`
	Note             *string  `json:"note,omitempty"`
	canonical        []byte
}

func (o Operation) CanonicalJSON() []byte { return append([]byte(nil), o.canonical...) }

type Receipt struct {
	OperationID string  `json:"operationId"`
	ContentHash string  `json:"contentHash"`
	Outcome     string  `json:"outcome"`
	Known       bool    `json:"known"`
	Code        *string `json:"code"`
}

type Event struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Date string `json:"date"`
}

type Race struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Bootstrap struct {
	ReserveOrigins *[]ReserveOrigin `json:"reserveOrigins,omitempty"`
	SchemaVersion  int              `json:"schemaVersion"`
	ScopeID        string           `json:"scopeId"`
	SourceKind     string           `json:"sourceKind"`
	Event          Event            `json:"event"`
	Races          []Race           `json:"races"`
	Registrations  []Registration   `json:"registrations"`
	BaselineID     string           `json:"baselineId"`
	FeedCursor     string           `json:"feedCursor,omitempty"`
}

type FeedChange struct {
	RegistrationID string        `json:"registrationId"`
	Before         *Registration `json:"before"`
	After          *Registration `json:"after"`
}

type FeedAction struct {
	ActionID   string       `json:"actionId"`
	Kind       string       `json:"kind"`
	Sequence   string       `json:"sequence"`
	RecordedAt string       `json:"recordedAt"`
	SourceCode string       `json:"sourceCode"`
	Outcome    string       `json:"outcome"`
	Code       *string      `json:"code"`
	Operation  *Operation   `json:"operation"`
	Changes    []FeedChange `json:"changes"`
	canonical  []byte
}

func (a FeedAction) CanonicalJSON() []byte { return append([]byte(nil), a.canonical...) }

type FeedCursor struct {
	After   string `json:"after"`
	Next    string `json:"next"`
	Head    string `json:"head"`
	HasMore bool   `json:"hasMore"`
}

type FeedPage struct {
	SchemaVersion int          `json:"schemaVersion"`
	ScopeID       string       `json:"scopeId"`
	Cursor        FeedCursor   `json:"cursor"`
	Actions       []FeedAction `json:"actions"`
}
