package service

import (
	"context"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

// Judge view: every read of the member's tag plus what each read derived —
// the desktop counterpart of run5's checkpoint-correction screen.

type MemberPass struct {
	LogID          string  `json:"log_id"`
	TimeMs         int64   `json:"time_ms"`
	Board          string  `json:"board"`
	Ant            int     `json:"ant"`
	RSSI           int     `json:"rssi"`
	DisabledAt     *int64  `json:"disabled_at"`
	CheckpointID   *string `json:"checkpoint_id"` // nil → read produced no result
	CheckpointName *string `json:"checkpoint_name"`
	CheckpointSort *int64  `json:"checkpoint_sort"`
}

type MemberPasses struct {
	Registration  MemberRegistration    `json:"member"`
	MemberID      string                `json:"member_id"`
	FirstName     string                `json:"first_name"`
	LastName      string                `json:"last_name"`
	Number        *int64                `json:"number"`
	Status        int                   `json:"status"`
	StartTimeMs   *int64                `json:"start_time_ms"`
	FinishTimeMs  *int64                `json:"finish_time_ms"`
	CleanTime     *string               `json:"clean_time"`
	Passes        []MemberPass          `json:"passes"`
	ManualResults []sqlite.ManualResult `json:"manual_results"`
}

// MemberRegistration is bundled with passes so the drawer's fields and timing
// come from one coherent read, rather than two independently committed views.
type MemberRegistration struct {
	ID         string  `json:"id"`
	RaceID     string  `json:"race_id"`
	CategoryID *string `json:"category_id"`
	Number     *int64  `json:"number"`
	EPC        *string `json:"epc"`
	FirstName  string  `json:"first_name"`
	LastName   string  `json:"last_name"`
	Gender     *string `json:"gender"`
	DOB        *string `json:"dob"`
	Team       *string `json:"team"`
	City       *string `json:"city"`
}

// LoadMemberPasses lists the member's reads in chronological order with the
// result (if any) each one produced.
func LoadMemberPasses(ctx context.Context, store *sqlite.Store, memberID string) (MemberPasses, error) {
	return loadMemberPasses(ctx, store, memberID, false)
}

// LoadAcceptedMemberPasses omits unaccepted and disabled raw history from the
// initial drawer response. Manual judge results remain available in both views.
func LoadAcceptedMemberPasses(ctx context.Context, store *sqlite.Store, memberID string) (MemberPasses, error) {
	return loadMemberPasses(ctx, store, memberID, true)
}

func loadMemberPasses(ctx context.Context, store *sqlite.Store, memberID string, acceptedOnly bool) (MemberPasses, error) {
	out := MemberPasses{MemberID: memberID, Passes: []MemberPass{}, ManualResults: []sqlite.ManualResult{}}

	member, err := store.GetMember(ctx, memberID)
	if err != nil {
		return out, err
	}
	out.Registration = MemberRegistration{ID: member.ID, RaceID: member.RaceID, CategoryID: member.CategoryID, Number: member.Number, EPC: member.EPC, FirstName: member.FirstName, LastName: member.LastName, Gender: member.Gender, DOB: member.DOB, Team: member.Team, City: member.City}
	out.FirstName = member.FirstName
	out.LastName = member.LastName
	out.Number = member.Number
	out.Status = int(member.Status)
	out.StartTimeMs = member.StartTimeMs
	out.FinishTimeMs = member.FinishTimeMs
	out.CleanTime = member.CleanTime

	manual, err := store.ListManualResultsForMember(ctx, member.EventID, memberID)
	if err != nil {
		return out, err
	}
	out.ManualResults = manual

	if member.EPC == nil || *member.EPC == "" {
		return out, nil // no tag — nothing to show
	}

	list := store.ListMemberPasses
	if acceptedOnly {
		list = store.ListAcceptedMemberPasses
	}
	passes, err := list(ctx, member.EventID, memberID, *member.EPC)
	if err != nil {
		return out, err
	}
	for _, p := range passes {
		out.Passes = append(out.Passes, MemberPass{
			LogID: p.LogID, TimeMs: p.TimeMs, Board: p.Board, Ant: p.Ant, RSSI: p.RSSI,
			DisabledAt: p.DisabledAt, CheckpointID: p.CheckpointID,
			CheckpointName: p.CheckpointName, CheckpointSort: p.CheckpointSort,
		})
	}
	return out, nil
}
