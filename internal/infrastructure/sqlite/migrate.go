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
	if err := addEventUseRaceDateForAge(db); err != nil {
		return err
	}
	return nil
}

// seedRaceCategories backfills the race↔category pivot from member.category_id
// for events imported before the pivot existed (pre-schema_version 2). It runs
// only when the pivot is empty, so it never fights a judge's later attach/detach
// — a detach is refused while members are still assigned, so an empty pivot
// alongside assigned members can only mean a pre-pivot database. A fresh or
// member-less event seeds nothing. Called from New() after the schema applies
// (the table must already exist).
func seedRaceCategories(db *sql.DB) error {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM race_categories`).Scan(&n); err != nil {
		return fmt.Errorf("count race_categories: %w", err)
	}
	if n > 0 {
		return nil
	}
	if _, err := db.Exec(`
		INSERT INTO race_categories (race_id, category_id)
		SELECT DISTINCT race_id, category_id FROM members
		WHERE category_id IS NOT NULL AND category_id != ''`); err != nil {
		return fmt.Errorf("seed race_categories: %w", err)
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

func addEventUseRaceDateForAge(db *sql.DB) error {
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('events') WHERE name = 'use_race_date_for_age'`).Scan(&count); err != nil {
		return fmt.Errorf("inspect events schema: %w", err)
	}
	if count > 0 {
		return nil
	}
	if _, err := db.Exec(`ALTER TABLE events ADD COLUMN use_race_date_for_age INTEGER NOT NULL DEFAULT 0`); err != nil {
		return fmt.Errorf("add events.use_race_date_for_age: %w", err)
	}
	return nil
}
