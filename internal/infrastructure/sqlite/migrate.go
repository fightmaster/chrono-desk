package sqlite

import (
	"database/sql"
	"fmt"
)

// migrate upgrades pre-existing event databases in place. The embedded
// schema.sql only creates missing objects; structural changes to existing
// tables live here.
func migrate(db *sql.DB) error {
	if err := relaxResultsCheckpointNotNull(db); err != nil {
		return err
	}
	return nil
}

// relaxResultsCheckpointNotNull drops the NOT NULL constraint from
// results.checkpoint_id (manual judge entries carry no checkpoint). SQLite
// cannot alter a constraint, so the table is rebuilt once.
func relaxResultsCheckpointNotNull(db *sql.DB) error {
	var notNull int
	err := db.QueryRow(`SELECT "notnull" FROM pragma_table_info('results') WHERE name = 'checkpoint_id'`).
		Scan(&notNull)
	if err == sql.ErrNoRows || notNull == 0 {
		return nil // fresh schema or already relaxed
	}
	if err != nil {
		return fmt.Errorf("inspect results schema: %w", err)
	}

	stmts := []string{
		`PRAGMA foreign_keys=OFF`,
		`BEGIN IMMEDIATE`,
		`CREATE TABLE results_new (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			event_id      TEXT NOT NULL REFERENCES events(id),
			race_id       TEXT NOT NULL REFERENCES races(id),
			member_id     TEXT NOT NULL REFERENCES members(id),
			checkpoint_id TEXT REFERENCES checkpoints(id),
			rfid_log_id   TEXT REFERENCES rfid_logs(id),
			time_ms       INTEGER NOT NULL,
			number        INTEGER
		)`,
		`INSERT INTO results_new SELECT id, event_id, race_id, member_id, checkpoint_id, rfid_log_id, time_ms, number FROM results`,
		`DROP TABLE results`,
		`ALTER TABLE results_new RENAME TO results`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_results_rfid_log ON results(rfid_log_id)`,
		`CREATE INDEX IF NOT EXISTS idx_results_member_time ON results(member_id, race_id, time_ms)`,
		`COMMIT`,
		`PRAGMA foreign_keys=ON`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			_, _ = db.Exec(`ROLLBACK`)
			return fmt.Errorf("relax results.checkpoint_id (%q): %w", stmt[:min(40, len(stmt))], err)
		}
	}
	return nil
}
