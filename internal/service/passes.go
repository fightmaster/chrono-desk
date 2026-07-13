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

// LoadMemberPasses lists the member's reads in chronological order with the
// result (if any) each one produced.
func LoadMemberPasses(ctx context.Context, store *sqlite.Store, memberID string) (MemberPasses, error) {
	out := MemberPasses{MemberID: memberID, Passes: []MemberPass{}, ManualResults: []sqlite.ManualResult{}}

	member, err := store.GetMember(ctx, memberID)
	if err != nil {
		return out, err
	}
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

	passes, err := store.ListMemberPasses(ctx, member.EventID, memberID, *member.EPC)
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
