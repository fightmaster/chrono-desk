package sqlite

import (
	"strings"
	"testing"
)

func TestLiveFinishIndexesUpgradeAndOrder(t *testing.T) {
	store := newTestStore(t)
	for _, name := range []string{"idx_members_live_finish", "idx_members_race_live_finish"} {
		if _, err := store.DB().Exec("DROP INDEX " + name); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate reopening an event created before the presenter feature.
	if err := migrate(store.DB()); err != nil {
		t.Fatal(err)
	}
	if err := migrate(store.DB()); err != nil {
		t.Fatal("migration not idempotent", err)
	}
	for _, filtered := range []bool{false, true} {
		query := `EXPLAIN QUERY PLAN SELECT m.id FROM members m WHERE m.event_id='event'
   AND m.status=0 AND m.finish_time_ms IS NOT NULL AND m.clean_time IS NOT NULL`
		if filtered {
			query += ` AND m.race_id='race'`
		}
		query += ` ORDER BY m.finish_time_ms DESC,m.id DESC LIMIT 10`
		rows, err := store.DB().Query(query)
		if err != nil {
			t.Fatal(err)
		}
		var plan string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan += detail
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		expected := "idx_members_live_finish"
		if filtered {
			expected = "idx_members_race_live_finish"
		}
		if !strings.Contains(plan, expected) || strings.Contains(plan, "TEMP B-TREE") {
			t.Fatalf("unbounded ordering: %s", plan)
		}
	}
}
