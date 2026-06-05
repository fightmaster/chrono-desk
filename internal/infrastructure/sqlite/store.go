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
	db *sql.DB
}

// New runs the embedded schema (idempotent) and returns a Store.
func New(db *sql.DB) (*Store, error) {
	if _, err := db.Exec(schemaSQL); err != nil {
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

// DB exposes the underlying handle for transaction composition.
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) UpsertEvent(ctx context.Context, e domain.Event) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO events (id, name, slug, date, timezone) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, slug=excluded.slug, date=excluded.date, timezone=excluded.timezone`,
		e.ID, e.Name, e.Slug, e.Date, e.Timezone)
	if err != nil {
		return fmt.Errorf("upsert event %s: %w", e.ID, err)
	}
	return nil
}

func (s *Store) GetEvent(ctx context.Context, id string) (domain.Event, error) {
	var e domain.Event
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, slug, date, timezone FROM events WHERE id = ?`, id).
		Scan(&e.ID, &e.Name, &e.Slug, &e.Date, &e.Timezone)
	if err != nil {
		return domain.Event{}, fmt.Errorf("get event %s: %w", id, err)
	}
	return e, nil
}

// InsertRfidLogs inserts logs idempotently (INSERT OR IGNORE on the md5 id)
// and reports how many rows were actually new.
func (s *Store) InsertRfidLogs(ctx context.Context, logs []domain.RfidLog) (inserted int64, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
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
