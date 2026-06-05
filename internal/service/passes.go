package service

import (
	"context"
	"database/sql"
	"fmt"

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

	var epc sql.NullString
	var eventID string
	err := store.DB().QueryRowContext(ctx, `
		SELECT event_id, epc, first_name, last_name, number, status, start_time_ms, finish_time_ms, clean_time
		FROM members WHERE id = ?`, memberID).
		Scan(&eventID, &epc, &out.FirstName, &out.LastName, &out.Number, &out.Status,
			&out.StartTimeMs, &out.FinishTimeMs, &out.CleanTime)
	if err != nil {
		return out, fmt.Errorf("member %s: %w", memberID, err)
	}
	manual, err := store.ListManualResults(ctx, eventID, "")
	if err != nil {
		return out, err
	}
	for _, m := range manual {
		if m.MemberID == memberID {
			out.ManualResults = append(out.ManualResults, m)
		}
	}

	if !epc.Valid || epc.String == "" {
		return out, nil // no tag — nothing to show
	}

	rows, err := store.DB().QueryContext(ctx, `
		SELECT l.id, l.time_ms, l.board, l.ant, l.rssi, l.disabled_at,
		       c.id, c.name, c.sort
		FROM rfid_logs l
		LEFT JOIN results r ON r.rfid_log_id = l.id AND r.member_id = ?
		LEFT JOIN checkpoints c ON c.id = r.checkpoint_id
		WHERE l.epc = ? AND l.event_id = (SELECT event_id FROM members WHERE id = ?)
		ORDER BY l.time_ms, l.id`, memberID, epc.String, memberID)
	if err != nil {
		return out, fmt.Errorf("passes for %s: %w", memberID, err)
	}
	defer rows.Close()

	for rows.Next() {
		var p MemberPass
		if err := rows.Scan(&p.LogID, &p.TimeMs, &p.Board, &p.Ant, &p.RSSI, &p.DisabledAt,
			&p.CheckpointID, &p.CheckpointName, &p.CheckpointSort); err != nil {
			return out, err
		}
		out.Passes = append(out.Passes, p)
	}
	return out, rows.Err()
}
