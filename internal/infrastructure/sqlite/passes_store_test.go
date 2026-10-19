package sqlite

import (
	"strings"
	"testing"
)

func TestAcceptedPassQueryDoesNotScanRawEventWithoutStatistics(t *testing.T) {
	store := newTestStore(t)
	rows, err := store.DB().Query("EXPLAIN QUERY PLAN "+acceptedMemberPassQuery, "member", "event", "alias")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	memberLookup, rawLookup := false, false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(detail, "SEARCH r USING INDEX idx_results_member_time (member_id=?)") {
			memberLookup = true
		}
		if strings.Contains(detail, "SEARCH l USING INDEX sqlite_autoindex_rfid_logs_1 (id=?)") {
			rawLookup = true
			if !memberLookup {
				t.Fatalf("raw lookup precedes member lookup: %s", detail)
			}
		}
		if strings.Contains(detail, "SCAN l") || strings.Contains(detail, "idx_rfid_logs_time") {
			t.Fatalf("initial drawer scans raw history: %s", detail)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !memberLookup || !rawLookup {
		t.Fatalf("expected indexed member then observation lookup: member=%t raw=%t", memberLookup, rawLookup)
	}
}
