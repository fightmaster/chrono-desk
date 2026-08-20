package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

//go:embed schema.sql
var schemaSQL string

// Store wraps one event database.
type Store struct {
	db                 database
	root               *sql.DB
	tx                 *sql.Tx
	originInstanceID   string
	nextOriginSequence func() (int64, error)
}

type StoreOption func(*Store)

// WithObservationOrigin configures the durable installation identity and its
// process-safe persistent sequence allocator. EventCatalog uses this option.
func WithObservationOrigin(instanceID string, next func() (int64, error)) StoreOption {
	return func(store *Store) {
		store.originInstanceID = instanceID
		store.nextOriginSequence = next
	}
}

// WithOriginInstanceID is a test/embedded convenience with an in-memory
// monotonic sequence. Production construction goes through EventCatalog and
// WithObservationOrigin so the sequence survives restarts and spans events.
func WithOriginInstanceID(instanceID string) StoreOption {
	var sequence atomic.Int64
	return WithObservationOrigin(instanceID, func() (int64, error) {
		return sequence.Add(1), nil
	})
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
func New(db *sql.DB, options ...StoreOption) (*Store, error) {
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
	store := &Store{db: db, root: db}
	for _, option := range options {
		option(store)
	}
	return store, nil
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

	txStore := &Store{
		db: tx, root: s.root, tx: tx,
		originInstanceID: s.originInstanceID, nextOriginSequence: s.nextOriginSequence,
	}
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
		INSERT OR IGNORE INTO rfid_logs (
			id, event_id, status, number, time_ms, ant, epc, rssi, board, disabled_at,
			observation_version, capture_source_id, origin_system, origin_instance_id, origin_sequence)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	for _, l := range logs {
		res, err := stmt.ExecContext(ctx,
			l.ID, l.EventID, l.Status, l.Number, l.TimeMs, l.Ant, l.EPC, l.RSSI, l.Board, l.DisabledAt,
			nullablePositiveInt(l.ObservationVersion), nullableString(l.CaptureSourceID), nullableString(l.OriginSystem),
			nullableString(l.OriginInstanceID), nullablePositiveInt64(l.OriginSequence))
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

// InsertOwnedRfidLogs inserts observations accepted locally by Chrono Desk and
// records their outbound ownership atomically. Existing/imported ids remain
// foreign: a duplicate never gains an outbox row or Chrono Desk provenance.
func (s *Store) InsertOwnedRfidLogs(ctx context.Context, logs []domain.RfidLog) (inserted int64, err error) {
	if strings.TrimSpace(s.originInstanceID) == "" || s.nextOriginSequence == nil {
		return 0, fmt.Errorf("observation origin is not configured")
	}
	tx, err := s.root.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	insertLog, err := tx.PrepareContext(ctx, `
		INSERT OR IGNORE INTO rfid_logs (id, event_id, status, number, time_ms, ant, epc, rssi, board, disabled_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, fmt.Errorf("prepare owned observation: %w", err)
	}
	defer insertLog.Close()

	for _, l := range logs {
		if strings.TrimSpace(l.CaptureSourceID) == "" {
			return 0, fmt.Errorf("rfid_log %s: capture source id is required", l.ID)
		}
		res, err := insertLog.ExecContext(ctx,
			l.ID, l.EventID, l.Status, l.Number, l.TimeMs, l.Ant, l.EPC, l.RSSI, l.Board, l.DisabledAt)
		if err != nil {
			return 0, fmt.Errorf("insert owned rfid_log %s: %w", l.ID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("rows affected: %w", err)
		}
		if n == 0 {
			continue
		}
		sequence, err := s.nextOriginSequence()
		if err != nil {
			return 0, fmt.Errorf("allocate origin sequence for %s: %w", l.ID, err)
		}
		if sequence <= 0 {
			return 0, fmt.Errorf("allocate origin sequence for %s: non-positive sequence", l.ID)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO observation_outbox (origin_sequence, observation_id, state, created_at)
			VALUES (?, ?, 'pending', ?)`, sequence, l.ID, time.Now().UnixMilli()); err != nil {
			return 0, fmt.Errorf("journal owned rfid_log %s: %w", l.ID, err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE rfid_logs SET observation_version = 1, capture_source_id = ?, origin_system = 'chrono-desk',
				origin_instance_id = ?, origin_sequence = ? WHERE id = ?`,
			l.CaptureSourceID, s.originInstanceID, sequence, l.ID); err != nil {
			return 0, fmt.Errorf("set origin for rfid_log %s: %w", l.ID, err)
		}
		inserted++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return inserted, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullablePositiveInt(value int) any {
	if value <= 0 {
		return nil
	}
	return value
}

func nullablePositiveInt64(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
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
