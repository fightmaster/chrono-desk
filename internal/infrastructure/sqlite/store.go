package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

//go:embed schema.sql
var schemaSQL string

// Store wraps one event database.
type Store struct {
	db   database
	root *sql.DB
	tx   *sql.Tx
}

// database is the query surface shared by *sql.DB and *sql.Tx.
type database interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// New runs the embedded schema (idempotent), applies in-place migrations for
// pre-existing databases and returns a Store.
func New(db *sql.DB) (*Store, error) {
	if _, err := db.Exec(schemaSQL); err != nil {
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrate(db); err != nil {
		return nil, err
	}
	// Backfill needs the schema applied first (it touches the new pivot table),
	// so it runs here rather than in migrate().
	if err := seedRaceCategories(db); err != nil {
		return nil, err
	}
	return &Store{db: db, root: db}, nil
}

// DB exposes the root handle to infrastructure adapters and test assertions.
// Application use cases should use Store methods and WithinTx instead.
func (s *Store) DB() *sql.DB { return s.root }

// Close releases the event database owned by the store.
func (s *Store) Close() error { return s.root.Close() }

// WithinTx runs fn against a transaction-bound Store and commits only when fn
// succeeds. Nested calls join the existing transaction.
func (s *Store) WithinTx(ctx context.Context, fn func(*Store) error) error {
	if s.tx != nil {
		return fn(s)
	}
	tx, err := s.root.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	txStore := &Store{db: tx, root: s.root, tx: tx}
	if err := fn(txStore); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// execer is satisfied by both *sql.DB and *sql.Tx, so the upsert helpers below
// run either standalone or inside the single import transaction
// (ApplyEventImport). The connection pool is capped at one (open.go), so an
// open tx and a parallel s.db call would deadlock — the import must route every
// write through its tx, which this interface makes possible without duplicating
// the upsert SQL.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (s *Store) UpsertEvent(ctx context.Context, e domain.Event) error {
	return upsertEvent(ctx, s.db, e)
}

func upsertEvent(ctx context.Context, ex execer, e domain.Event) error {
	_, err := ex.ExecContext(ctx, `
		INSERT INTO events (id, name, slug, date, timezone, use_race_date_for_age) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, slug=excluded.slug, date=excluded.date,
			timezone=excluded.timezone, use_race_date_for_age=excluded.use_race_date_for_age`,
		e.ID, e.Name, e.Slug, e.Date, e.Timezone, e.UseRaceDateForAge)
	if err != nil {
		return fmt.Errorf("upsert event %s: %w", e.ID, err)
	}
	return nil
}

func (s *Store) GetEvent(ctx context.Context, id string) (domain.Event, error) {
	var e domain.Event
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, slug, date, timezone, use_race_date_for_age FROM events WHERE id = ?`, id).
		Scan(&e.ID, &e.Name, &e.Slug, &e.Date, &e.Timezone, &e.UseRaceDateForAge)
	if err != nil {
		return domain.Event{}, fmt.Errorf("get event %s: %w", id, err)
	}
	return e, nil
}

// FirstEvent returns the single event header stored in an event database.
func (s *Store) FirstEvent(ctx context.Context) (domain.Event, error) {
	var e domain.Event
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, slug, date, timezone, use_race_date_for_age FROM events LIMIT 1`).
		Scan(&e.ID, &e.Name, &e.Slug, &e.Date, &e.Timezone, &e.UseRaceDateForAge)
	if err != nil {
		return domain.Event{}, fmt.Errorf("read event header: %w", err)
	}
	return e, nil
}

// InsertRfidLogs inserts logs idempotently (INSERT OR IGNORE on the md5 id)
// and reports how many rows were actually new.
func (s *Store) InsertRfidLogs(ctx context.Context, logs []domain.RfidLog) (inserted int64, err error) {
	tx, err := s.root.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR IGNORE INTO rfid_logs (id, event_id, status, number, time_ms, ant, epc, rssi, board, disabled_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	for _, l := range logs {
		res, err := stmt.ExecContext(ctx,
			l.ID, l.EventID, l.Status, l.Number, l.TimeMs, l.Ant, l.EPC, l.RSSI, l.Board, l.DisabledAt)
		if err != nil {
			return 0, fmt.Errorf("insert rfid_log %s: %w", l.ID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("rows affected: %w", err)
		}
		inserted += n
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return inserted, nil
}

func (s *Store) CountRfidLogs(ctx context.Context, eventID string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM rfid_logs WHERE event_id = ?`, eventID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count rfid_logs: %w", err)
	}
	return n, nil
}
