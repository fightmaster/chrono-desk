package sqlite

import (
	"database/sql"
	"fmt"
	"strings"
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
	if err := addRfidObservationOrigin(db); err != nil {
		return err
	}
	if err := addObservationOutboxBatchID(db); err != nil {
		return err
	}
	if err := addSyncPullCursor(db); err != nil {
		return err
	}
	if err := addMemberStartProvenance(db); err != nil {
		return err
	}
	if err := addProjectionRevisionFence(db); err != nil {
		return err
	}
	return nil
}

func addProjectionRevisionFence(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS projection_revisions (
		event_id TEXT PRIMARY KEY,
		config_revision INTEGER NOT NULL DEFAULT 0,
		input_revision INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		return fmt.Errorf("create projection revisions: %w", err)
	}

	type triggerSet struct {
		table, revision, eventColumn       string
		insertWhen, updateWhen, deleteWhen string
	}
	for _, set := range []triggerSet{
		{
			table: "events", revision: "config_revision", eventColumn: "id",
			updateWhen: columnsChanged("id", "name", "slug", "date", "timezone", "use_race_date_for_age"),
		},
		{
			table: "races", revision: "config_revision",
			updateWhen: columnsChanged("id", "event_id", "date", "started_at_ms", "format", "time_limit_seconds", "category_excludes_top_by_gender"),
		},
		{
			table: "checkpoints", revision: "config_revision",
			updateWhen: columnsChanged("id", "event_id", "race_id", "type", "sort", "board", "since_ms", "since_offset_seconds", "sleep_after_prev_seconds"),
		},
		{
			table: "members", revision: "config_revision",
			updateWhen: columnsChanged("id", "event_id", "race_id", "category_id", "number", "epc", "gender", "dob", "status", "start_time_ms", "start_time_source", "start_observation_id", "finish_time_ms"),
		},
		{
			table: "rfid_logs", revision: "input_revision",
			updateWhen: columnsChanged("id", "event_id", "status", "number", "time_ms", "ant", "epc", "rssi", "board", "disabled_at", "observation_version", "capture_source_id", "origin_system", "origin_instance_id", "origin_sequence"),
		},
		{
			table: "results", revision: "input_revision",
			insertWhen: "NEW.rfid_log_id IS NULL AND NEW.checkpoint_id IS NULL",
			updateWhen: "((OLD.rfid_log_id IS NULL AND OLD.checkpoint_id IS NULL) OR (NEW.rfid_log_id IS NULL AND NEW.checkpoint_id IS NULL)) AND (" + columnsChanged("id", "event_id", "race_id", "member_id", "time_ms", "number", "rfid_log_id", "checkpoint_id") + ")",
			deleteWhen: "OLD.rfid_log_id IS NULL AND OLD.checkpoint_id IS NULL",
		},
		{
			table: "sync_config", revision: "input_revision",
			insertWhen: "NEW.pull_cursor IS NOT NULL",
			updateWhen: "OLD.pull_cursor IS NOT NEW.pull_cursor",
			deleteWhen: "OLD.pull_cursor IS NOT NULL",
		},
	} {
		if set.eventColumn == "" {
			set.eventColumn = "event_id"
		}
		if err := createEventRevisionTriggers(db, set.table, set.revision, set.eventColumn, set.insertWhen, set.updateWhen, set.deleteWhen); err != nil {
			return err
		}
	}

	for name, statement := range map[string]string{
		"categories_update": `CREATE TRIGGER IF NOT EXISTS projection_revision_v1_categories_update
			AFTER UPDATE ON categories
			WHEN ` + columnsChanged("id", "name", "min", "max", "gender") + ` BEGIN
				INSERT INTO projection_revisions (event_id, config_revision)
				SELECT event_id, 1 FROM (
					SELECT r.event_id FROM race_categories rc JOIN races r ON r.id=rc.race_id
					WHERE rc.category_id IN (OLD.id, NEW.id)
					UNION
					SELECT m.event_id FROM members m WHERE m.category_id IN (OLD.id, NEW.id)
				) WHERE event_id IS NOT NULL
				ON CONFLICT(event_id) DO UPDATE SET config_revision=config_revision+1;
			END`,
		"race_categories_insert": `CREATE TRIGGER IF NOT EXISTS projection_revision_v1_race_categories_insert
			AFTER INSERT ON race_categories BEGIN
				INSERT INTO projection_revisions (event_id, config_revision)
				SELECT event_id, 1 FROM races WHERE id=NEW.race_id
				ON CONFLICT(event_id) DO UPDATE SET config_revision=config_revision+1;
			END`,
		"race_categories_update": `CREATE TRIGGER IF NOT EXISTS projection_revision_v1_race_categories_update
			AFTER UPDATE ON race_categories
			WHEN ` + columnsChanged("race_id", "category_id") + ` BEGIN
				INSERT INTO projection_revisions (event_id, config_revision)
				SELECT event_id, 1 FROM races WHERE id IN (OLD.race_id, NEW.race_id)
				ON CONFLICT(event_id) DO UPDATE SET config_revision=config_revision+1;
			END`,
		"race_categories_delete": `CREATE TRIGGER IF NOT EXISTS projection_revision_v1_race_categories_delete
			AFTER DELETE ON race_categories BEGIN
				INSERT INTO projection_revisions (event_id, config_revision)
				SELECT event_id, 1 FROM races WHERE id=OLD.race_id
				ON CONFLICT(event_id) DO UPDATE SET config_revision=config_revision+1;
			END`,
	} {
		if _, err := db.Exec(statement); err != nil {
			return fmt.Errorf("create projection revision trigger %s: %w", name, err)
		}
	}
	return nil
}

func columnsChanged(columns ...string) string {
	parts := make([]string, 0, len(columns))
	for _, column := range columns {
		parts = append(parts, "OLD."+column+" IS NOT NEW."+column)
	}
	return strings.Join(parts, " OR ")
}

func createEventRevisionTriggers(db *sql.DB, table, revision, eventColumn, insertWhen, updateWhen, deleteWhen string) error {
	when := func(condition string) string {
		if condition == "" {
			return ""
		}
		return " WHEN " + condition
	}
	statements := map[string]string{
		"insert": fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS projection_revision_v1_%s_insert
			AFTER INSERT ON %s%s BEGIN
				INSERT INTO projection_revisions (event_id, %s) VALUES (NEW.%s, 1)
				ON CONFLICT(event_id) DO UPDATE SET %s=%s+1;
			END`, table, table, when(insertWhen), revision, eventColumn, revision, revision),
		"update": fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS projection_revision_v1_%s_update
			AFTER UPDATE ON %s%s BEGIN
				INSERT INTO projection_revisions (event_id)
				SELECT event_id FROM (SELECT OLD.%s AS event_id UNION SELECT NEW.%s) WHERE event_id IS NOT NULL
				ON CONFLICT(event_id) DO NOTHING;
				UPDATE projection_revisions SET %s=%s+1 WHERE event_id IN (OLD.%s, NEW.%s);
			END`, table, table, when(updateWhen), eventColumn, eventColumn, revision, revision, eventColumn, eventColumn),
		"delete": fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS projection_revision_v1_%s_delete
			AFTER DELETE ON %s%s BEGIN
				INSERT INTO projection_revisions (event_id, %s) VALUES (OLD.%s, 1)
				ON CONFLICT(event_id) DO UPDATE SET %s=%s+1;
			END`, table, table, when(deleteWhen), revision, eventColumn, revision, revision),
	}
	for action, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return fmt.Errorf("create projection revision trigger %s.%s: %w", table, action, err)
		}
	}
	return nil
}

func addMemberStartProvenance(db *sql.DB) error {
	for _, column := range []struct {
		name, typeSQL string
	}{
		{"start_time_source", "TEXT NOT NULL DEFAULT 'unknown'"},
		{"start_observation_id", "TEXT"},
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('members') WHERE name = ?`, column.name).Scan(&count); err != nil {
			return fmt.Errorf("inspect members.%s: %w", column.name, err)
		}
		if count == 0 {
			if _, err := db.Exec(`ALTER TABLE members ADD COLUMN ` + column.name + ` ` + column.typeSQL); err != nil {
				return fmt.Errorf("add members.%s: %w", column.name, err)
			}
		}
	}
	return nil
}

func addSyncPullCursor(db *sql.DB) error {
	for _, column := range []struct {
		name, typeSQL string
	}{
		{"pull_cursor", "TEXT"},
		{"last_pulled_at", "INTEGER"},
		{"projection_pending", "INTEGER NOT NULL DEFAULT 0"},
	} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('sync_config') WHERE name = ?`, column.name).Scan(&count); err != nil {
			return fmt.Errorf("inspect sync_config.%s: %w", column.name, err)
		}
		if count == 0 {
			if _, err := db.Exec(`ALTER TABLE sync_config ADD COLUMN ` + column.name + ` ` + column.typeSQL); err != nil {
				return fmt.Errorf("add sync_config.%s: %w", column.name, err)
			}
		}
	}
	return nil
}

func addObservationOutboxBatchID(db *sql.DB) error {
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('observation_outbox') WHERE name = 'batch_id'`).Scan(&count); err != nil {
		return fmt.Errorf("inspect observation_outbox.batch_id: %w", err)
	}
	if count == 0 {
		if _, err := db.Exec(`ALTER TABLE observation_outbox ADD COLUMN batch_id TEXT`); err != nil {
			return fmt.Errorf("add observation_outbox.batch_id: %w", err)
		}
	}
	if _, err := db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_observation_outbox_batch
		ON observation_outbox(batch_id, state, origin_sequence)`); err != nil {
		return fmt.Errorf("index observation_outbox batch: %w", err)
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

func addRfidObservationOrigin(db *sql.DB) error {
	columns := []struct {
		name    string
		typeSQL string
	}{
		{"observation_version", "INTEGER"},
		{"capture_source_id", "TEXT"},
		{"origin_system", "TEXT"},
		{"origin_instance_id", "TEXT"},
		{"origin_sequence", "INTEGER"},
	}
	for _, column := range columns {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('rfid_logs') WHERE name = ?`, column.name).Scan(&count); err != nil {
			return fmt.Errorf("inspect rfid_logs.%s: %w", column.name, err)
		}
		if count > 0 {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE rfid_logs ADD COLUMN ` + column.name + ` ` + column.typeSQL); err != nil {
			return fmt.Errorf("add rfid_logs.%s: %w", column.name, err)
		}
	}
	return nil
}
