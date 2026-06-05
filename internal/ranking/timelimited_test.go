package ranking

import (
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

func iptr(v int64) *int64 { return &v }

func tlMember(id string, startMs int64) domain.Member {
	return domain.Member{ID: id, StartTimeMs: &startMs}
}

// Ordering ports rfid-sync's TimeLimitedMemberResult + run5 rankRows:
// further checkpoint first (sort DESC), then faster elapsed (-elapsedMs DESC).
func TestTimeLimitedProtocolOrdering(t *testing.T) {
	limit := int64(3600)
	race := domain.Race{Format: domain.FormatTimeLimited, TimeLimitSeconds: &limit}

	members := []domain.Member{
		tlMember("reached-cp2-slow", 0),
		tlMember("reached-cp3", 0),
		tlMember("reached-cp2-fast", 0),
		tlMember("no-pass", 0),                // never read → absent
		{ID: "dnf", Status: domain.StatusDNF}, // judge status → non-ok row
		{ID: "no-start"},                      // no window → absent
	}
	passes := map[string]LastPass{
		"reached-cp2-slow": {TimeMs: 50 * 60 * 1000, CheckpointSort: iptr(2), CheckpointName: sptr("CP2")},
		"reached-cp3":      {TimeMs: 59 * 60 * 1000, CheckpointSort: iptr(3), CheckpointName: sptr("CP3")},
		"reached-cp2-fast": {TimeMs: 40 * 60 * 1000, CheckpointSort: iptr(2), CheckpointName: sptr("CP2")},
	}

	rows := Protocol(race, members, passes)

	wantOrder := []string{"reached-cp3", "reached-cp2-fast", "reached-cp2-slow", "dnf"}
	if len(rows) != len(wantOrder) {
		t.Fatalf("rows = %d, want %d", len(rows), len(wantOrder))
	}
	for i, want := range wantOrder {
		if rows[i].Member.ID != want {
			t.Errorf("rows[%d] = %s, want %s", i, rows[i].Member.ID, want)
		}
	}

	if rows[0].Place == nil || *rows[0].Place != 1 {
		t.Errorf("cp3 place = %v, want 1", rows[0].Place)
	}
	if rows[1].ElapsedMs == nil || *rows[1].ElapsedMs != 40*60*1000 {
		t.Errorf("cp2-fast elapsed = %v", rows[1].ElapsedMs)
	}
	if rows[0].LastCheckpointName == nil || *rows[0].LastCheckpointName != "CP3" {
		t.Errorf("cp3 checkpoint name = %v", rows[0].LastCheckpointName)
	}
	if rows[3].Place != nil {
		t.Errorf("dnf place = %v, want nil", rows[3].Place)
	}
}

// A pass timestamped before the member's start clamps elapsed at 0 — the
// reference implementation never emits negative elapsed.
func TestTimeLimitedClampsNegativeElapsed(t *testing.T) {
	limit := int64(3600)
	race := domain.Race{Format: domain.FormatTimeLimited, TimeLimitSeconds: &limit}
	members := []domain.Member{tlMember("m", 100_000)}
	passes := map[string]LastPass{"m": {TimeMs: 90_000, CheckpointSort: iptr(1)}}

	rows := Protocol(race, members, passes)
	if len(rows) != 1 || rows[0].ElapsedMs == nil || *rows[0].ElapsedMs != 0 {
		t.Fatalf("rows = %+v, want one row with elapsed 0", rows)
	}
}

// Zero or missing time limit → no window → no ok rows (mirrors the
// skip_member_result_no_window guard).
func TestTimeLimitedRequiresWindow(t *testing.T) {
	race := domain.Race{Format: domain.FormatTimeLimited}
	members := []domain.Member{tlMember("m", 0)}
	passes := map[string]LastPass{"m": {TimeMs: 1000, CheckpointSort: iptr(1)}}

	if rows := Protocol(race, members, passes); len(rows) != 0 {
		t.Fatalf("rows = %+v, want none without a time limit", rows)
	}
}
