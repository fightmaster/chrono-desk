package service

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"testing"
)

// Golden test on a real production event: "XIV Вертикальный километр"
// (2026-05-30, event 621632) — 5 fixed_distance lap-counting races, 8474 rfid
// logs incl. 36 judge-disabled ones, sleep/since_offset checkpoint guards.
//
// The reference snapshot was taken from the local run5 MySQL immediately
// after a full `php artisan event:recount 621632` (2026-06-05), so it is the
// canonical PHP-recount output, not the live-ingest state (live processing
// rejects late-arriving flash-drive logs that a chronological recount
// accepts). The fixture carries member start_time (site-owned, survives
// recounts — e.g. one manually staggered start) but strips finish/clean; the
// offline recount must reproduce the site's derived finish/clean times and
// per-member result counts byte-for-byte.
//
// Regenerate fixtures from the local run5 MySQL when the contract changes —
// run event:recount first, then the generator (/tmp/golden-gen pattern, see
// git history of this file's commit).

type goldenExpectations struct {
	Members []struct {
		MemberID     string  `json:"member_id"`
		RaceID       string  `json:"race_id"`
		StartMs      *int64  `json:"start_ms"`
		FinishMs     *int64  `json:"finish_ms"`
		CleanTime    *string `json:"clean_time"`
		ResultsCount int64   `json:"results_count"`
	} `json:"members"`
}

func openGz(t *testing.T, path string) io.Reader {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { f.Close() })
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gunzip %s: %v", path, err)
	}
	return gz
}

func TestGoldenEventRecountMatchesProduction(t *testing.T) {
	if testing.Short() {
		t.Skip("golden replay is not short")
	}
	store := newTestStore(t)
	ctx := context.Background()

	export, err := ParseEventExport(openGz(t, "testdata/golden/event-621632.json.gz"))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	stats, err := NewEventImporter(store).Import(ctx, export)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if stats.Members == 0 || stats.RfidLogs == 0 {
		t.Fatalf("suspicious import stats: %+v", stats)
	}

	rec := NewRecounter(store, log.New(io.Discard, "", 0), false)
	if _, err := rec.Recount(ctx, export.Event.ID, ""); err != nil {
		t.Fatalf("recount: %v", err)
	}

	var want goldenExpectations
	if err := json.NewDecoder(openGz(t, "testdata/golden/expected-621632.json.gz")).Decode(&want); err != nil {
		t.Fatalf("parse expectations: %v", err)
	}

	mismatches := 0
	for _, exp := range want.Members {
		var start, finish *int64
		var clean *string
		var cnt int64
		err := store.DB().QueryRow(`
			SELECT m.start_time_ms, m.finish_time_ms, m.clean_time,
			       (SELECT COUNT(*) FROM results r WHERE r.member_id = m.id AND r.rfid_log_id IS NOT NULL)
			FROM members m WHERE m.id = ?`, exp.MemberID).
			Scan(&start, &finish, &clean, &cnt)
		if err != nil {
			t.Fatalf("member %s: %v", exp.MemberID, err)
		}

		if !int64PtrEq(start, exp.StartMs) {
			t.Errorf("member %s start = %v, want %v", exp.MemberID, fmtPtr(start), fmtPtr(exp.StartMs))
			mismatches++
		}
		if !int64PtrEq(finish, exp.FinishMs) {
			t.Errorf("member %s finish = %v, want %v", exp.MemberID, fmtPtr(finish), fmtPtr(exp.FinishMs))
			mismatches++
		}
		if !strPtrEq(clean, exp.CleanTime) {
			t.Errorf("member %s clean = %v, want %v", exp.MemberID, fmtSPtr(clean), fmtSPtr(exp.CleanTime))
			mismatches++
		}
		if cnt != exp.ResultsCount {
			t.Errorf("member %s results = %d, want %d", exp.MemberID, cnt, exp.ResultsCount)
			mismatches++
		}
		if mismatches > 25 {
			t.Fatal("too many mismatches, aborting diff output")
		}
	}
}

func int64PtrEq(a, b *int64) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	return a == nil || *a == *b
}

func strPtrEq(a, b *string) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	return a == nil || *a == *b
}

func fmtPtr(v *int64) any {
	if v == nil {
		return "<nil>"
	}
	return *v
}

func fmtSPtr(v *string) any {
	if v == nil {
		return "<nil>"
	}
	return *v
}
