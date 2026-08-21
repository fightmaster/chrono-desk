package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

const productionSizedEvidenceRows = 11_000

// BenchmarkProjectionEvidenceElevenThousand measures the exact snapshot fence
// at the event size that motivated the Stage 5 audit. Setup is outside the
// timed section; one iteration represents one complete evidence scan.
func BenchmarkProjectionEvidenceElevenThousand(b *testing.B) {
	store := newProjectionEvidenceBenchmarkStore(b)

	b.ReportAllocs()
	b.ReportMetric(productionSizedEvidenceRows, "members")
	b.ReportMetric(productionSizedEvidenceRows, "observations")
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		if _, err := store.ProjectionEvidence(context.Background(), "event-benchmark"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkProjectionRevisionsElevenThousand(b *testing.B) {
	store := newProjectionEvidenceBenchmarkStore(b)

	b.ReportAllocs()
	b.ReportMetric(productionSizedEvidenceRows, "members")
	b.ReportMetric(productionSizedEvidenceRows, "observations")
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		if _, err := store.ProjectionRevisions(context.Background(), "event-benchmark"); err != nil {
			b.Fatal(err)
		}
	}
}

func newProjectionEvidenceBenchmarkStore(b *testing.B) *Store {
	b.Helper()
	db, err := Open(filepath.Join(b.TempDir(), "evidence-benchmark.chrono"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })
	store, err := New(db)
	if err != nil {
		b.Fatal(err)
	}
	seedProjectionEvidenceBenchmark(b, store)
	return store
}

func seedProjectionEvidenceBenchmark(b *testing.B, store *Store) {
	b.Helper()
	tx, err := store.DB().Begin()
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	statements := []string{
		`INSERT INTO events (id, name, date, timezone) VALUES ('event-benchmark', 'Benchmark', '2026-08-21', 'Europe/Saratov')`,
		`INSERT INTO races (id, event_id, name, date, format) VALUES ('race-benchmark', 'event-benchmark', '11k', '2026-08-21 09:00:00', 'FixedDistance')`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			b.Fatal(err)
		}
	}
	for checkpoint := 1; checkpoint <= 5; checkpoint++ {
		checkpointType := 2
		if checkpoint == 5 {
			checkpointType = 3
		}
		if _, err := tx.Exec(`INSERT INTO checkpoints
			(id, event_id, race_id, name, type, sort, board)
			VALUES (?, 'event-benchmark', 'race-benchmark', ?, ?, ?, ?)`,
			fmt.Sprintf("checkpoint-%02d", checkpoint),
			fmt.Sprintf("Point %d", checkpoint),
			checkpointType,
			checkpoint,
			fmt.Sprintf("board-%02d", checkpoint),
		); err != nil {
			b.Fatal(err)
		}
	}

	memberStatement, err := tx.Prepare(`INSERT INTO members
		(id, event_id, race_id, number, epc, first_name, last_name, gender, dob)
		VALUES (?, 'event-benchmark', 'race-benchmark', ?, ?, 'Runner', ?, ?, '1990-01-01')`)
	if err != nil {
		b.Fatal(err)
	}
	defer memberStatement.Close()
	logStatement, err := tx.Prepare(`INSERT INTO rfid_logs
		(id, event_id, number, time_ms, epc, board, observation_version,
		 capture_source_id, origin_system, origin_instance_id, origin_sequence)
		VALUES (?, 'event-benchmark', ?, ?, ?, ?, 1, ?, 'chrono-desk', 'benchmark-desk', ?)`)
	if err != nil {
		b.Fatal(err)
	}
	defer logStatement.Close()

	for row := 1; row <= productionSizedEvidenceRows; row++ {
		memberID := fmt.Sprintf("member-%05d", row)
		epc := fmt.Sprintf("EPC%08d", row)
		gender := "male"
		if row%2 == 0 {
			gender = "female"
		}
		if _, err := memberStatement.Exec(memberID, row, epc, memberID, gender); err != nil {
			b.Fatal(err)
		}
		board := fmt.Sprintf("board-%02d", row%5+1)
		observationID := fmt.Sprintf("observation-%05d", row)
		if _, err := logStatement.Exec(
			observationID,
			row,
			int64(1_800_000_000_000+row),
			epc,
			board,
			"chrono-desk:event-benchmark:"+board,
			row,
		); err != nil {
			b.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
}
