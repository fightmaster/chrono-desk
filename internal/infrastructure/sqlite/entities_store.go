package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

// Entity upserts used by the event importer. All keyed on run5 string ids so
// re-imports overwrite site-owned data in place.

func (s *Store) UpsertLap(ctx context.Context, l domain.Lap) error {
	return upsertLap(ctx, s.db, l)
}

func upsertLap(ctx context.Context, ex execer, l domain.Lap) error {
	_, err := ex.ExecContext(ctx, `
		INSERT INTO laps (id, name, slug, description) VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, slug=excluded.slug, description=excluded.description`,
		l.ID, l.Name, l.Slug, l.Description)
	if err != nil {
		return fmt.Errorf("upsert lap %s: %w", l.ID, err)
	}
	return nil
}

func (s *Store) UpsertRace(ctx context.Context, r domain.Race) error {
	return upsertRace(ctx, s.db, r)
}

func upsertRace(ctx context.Context, ex execer, r domain.Race) error {
	_, err := ex.ExecContext(ctx, `
		INSERT INTO races (id, event_id, name, date, started_at_ms, lap_id, format, time_limit_seconds, category_excludes_top_by_gender)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			event_id=excluded.event_id, name=excluded.name, date=excluded.date,
			started_at_ms=excluded.started_at_ms, lap_id=excluded.lap_id, format=excluded.format,
			time_limit_seconds=excluded.time_limit_seconds,
			category_excludes_top_by_gender=excluded.category_excludes_top_by_gender`,
		r.ID, r.EventID, r.Name, r.Date, r.StartedAtMs, r.LapID, string(r.Format), r.TimeLimitSeconds, r.CategoryExcludesTopByGender)
	if err != nil {
		return fmt.Errorf("upsert race %s: %w", r.ID, err)
	}
	return nil
}

func (s *Store) UpsertCategory(ctx context.Context, c domain.Category) error {
	return upsertCategory(ctx, s.db, c)
}

func upsertCategory(ctx context.Context, ex execer, c domain.Category) error {
	_, err := ex.ExecContext(ctx, `
		INSERT INTO categories (id, name, min, max, gender) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, min=excluded.min, max=excluded.max, gender=excluded.gender`,
		c.ID, c.Name, c.Min, c.Max, c.Gender)
	if err != nil {
		return fmt.Errorf("upsert category %s: %w", c.ID, err)
	}
	return nil
}

func (s *Store) UpsertCheckpoint(ctx context.Context, cp domain.Checkpoint) error {
	return upsertCheckpoint(ctx, s.db, cp)
}

func upsertCheckpoint(ctx context.Context, ex execer, cp domain.Checkpoint) error {
	_, err := ex.ExecContext(ctx, `
		INSERT INTO checkpoints (id, event_id, race_id, name, type, sort, board, since_ms, since_offset_seconds, sleep_after_prev_seconds)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			event_id=excluded.event_id, race_id=excluded.race_id, name=excluded.name,
			type=excluded.type, sort=excluded.sort, board=excluded.board, since_ms=excluded.since_ms,
			since_offset_seconds=excluded.since_offset_seconds,
			sleep_after_prev_seconds=excluded.sleep_after_prev_seconds`,
		cp.ID, cp.EventID, cp.RaceID, cp.Name, int(cp.Type), cp.Sort, cp.Board, cp.SinceMs, cp.SinceOffsetSeconds, cp.SleepAfterPrevSeconds)
	if err != nil {
		return fmt.Errorf("upsert checkpoint %s: %w", cp.ID, err)
	}
	return nil
}

// MemberStartShift records one member whose start moved by the race-start
// delta, with both values so the service can journal the diff.
type MemberStartShift struct {
	MemberID   string
	OldStartMs int64
	NewStartMs int64
}

// ShiftMemberStarts moves every member of the race that has a start by deltaMs
// (the race start moved later/earlier, so the whole field follows). A relative
// shift preserves the gaps of a staggered start (раздельный старт каждые 30 с),
// unlike snapping everyone to one time. NULL starts are left alone — they
// re-derive to the current race start on the next recount. Returns the affected
// members with old/new for journaling.
func (s *Store) ShiftMemberStarts(ctx context.Context, raceID string, deltaMs int64) ([]MemberStartShift, error) {
	if deltaMs == 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin shift starts: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	rows, err := tx.QueryContext(ctx,
		`SELECT id, start_time_ms FROM members
		 WHERE race_id = ? AND start_time_ms IS NOT NULL
		 ORDER BY id`, raceID)
	if err != nil {
		return nil, fmt.Errorf("list members with start: %w", err)
	}
	var shifts []MemberStartShift
	for rows.Next() {
		var sh MemberStartShift
		if err := rows.Scan(&sh.MemberID, &sh.OldStartMs); err != nil {
			rows.Close()
			return nil, err
		}
		sh.NewStartMs = sh.OldStartMs + deltaMs
		shifts = append(shifts, sh)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	if len(shifts) > 0 {
		if _, err := tx.ExecContext(ctx,
			`UPDATE members SET start_time_ms = start_time_ms + ?
			 WHERE race_id = ? AND start_time_ms IS NOT NULL`,
			deltaMs, raceID); err != nil {
			return nil, fmt.Errorf("shift member starts: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit shift starts: %w", err)
	}
	return shifts, nil
}

func (s *Store) UpsertMember(ctx context.Context, m domain.Member) error {
	return upsertMember(ctx, s.db, m)
}

// UpdateMemberFinish applies a judge-entered finish. A non-nil start is persisted
// when the race start had to be used as the member's missing start reference.
func (s *Store) UpdateMemberFinish(ctx context.Context, memberID string, start *int64, finish int64, clean *string) error {
	var err error
	if start != nil {
		_, err = s.db.ExecContext(ctx,
			`UPDATE members SET start_time_ms = ?, finish_time_ms = ?, clean_time = ? WHERE id = ?`,
			*start, finish, clean, memberID)
	} else {
		_, err = s.db.ExecContext(ctx,
			`UPDATE members SET finish_time_ms = ?, clean_time = ? WHERE id = ?`,
			finish, clean, memberID)
	}
	if err != nil {
		return fmt.Errorf("update member finish: %w", err)
	}
	return nil
}

func upsertMember(ctx context.Context, ex execer, m domain.Member) error {
	_, err := ex.ExecContext(ctx, `
		INSERT INTO members (id, event_id, race_id, category_id, number, epc, rfid, first_name, last_name,
			gender, dob, city, team, status, start_time_ms, finish_time_ms, clean_time)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			event_id=excluded.event_id, race_id=excluded.race_id, category_id=excluded.category_id,
			number=excluded.number, epc=excluded.epc, rfid=excluded.rfid,
			first_name=excluded.first_name, last_name=excluded.last_name, gender=excluded.gender,
			dob=excluded.dob, city=excluded.city, team=excluded.team, status=excluded.status,
			start_time_ms=excluded.start_time_ms, finish_time_ms=excluded.finish_time_ms,
			clean_time=excluded.clean_time`,
		m.ID, m.EventID, m.RaceID, m.CategoryID, m.Number, m.EPC, m.RFID, m.FirstName, m.LastName,
		m.Gender, m.DOB, m.City, m.Team, int(m.Status), m.StartTimeMs, m.FinishTimeMs, m.CleanTime)
	if err != nil {
		return fmt.Errorf("upsert member %s: %w", m.ID, err)
	}
	return nil
}

// rfidLogUpsertSQL is the event-import variant: unlike InsertRfidLogs it also
// refreshes disabled_at on existing rows, so a re-export that disables a log
// (run5 ADR-0007) takes effect on the next recount.
const rfidLogUpsertSQL = `
	INSERT INTO rfid_logs (id, event_id, status, number, time_ms, ant, epc, rssi, board, disabled_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET disabled_at=excluded.disabled_at`

// UpsertRfidLogs upserts logs in their own transaction (standalone callers).
func (s *Store) UpsertRfidLogs(ctx context.Context, logs []domain.RfidLog) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	if err := upsertRfidLogs(ctx, tx, logs); err != nil {
		return err
	}
	return tx.Commit()
}

// upsertRfidLogs upserts every log on ex (a tx), using a prepared statement so
// a full event's worth of logs (thousands) stays fast.
func upsertRfidLogs(ctx context.Context, tx *sql.Tx, logs []domain.RfidLog) error {
	stmt, err := tx.PrepareContext(ctx, rfidLogUpsertSQL)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	for _, l := range logs {
		if _, err := stmt.ExecContext(ctx,
			l.ID, l.EventID, l.Status, l.Number, l.TimeMs, l.Ant, l.EPC, l.RSSI, l.Board, l.DisabledAt); err != nil {
			return fmt.Errorf("upsert rfid_log %s: %w", l.ID, err)
		}
	}
	return nil
}

// EventImportData is a fully-parsed event export, ready to apply atomically.
type EventImportData struct {
	Event       domain.Event
	Laps        []domain.Lap
	Races       []domain.Race
	Categories  []domain.Category
	Checkpoints []domain.Checkpoint
	Members     []domain.Member
	RfidLogs    []domain.RfidLog
	// CategoryRaces is the race↔category pivot from the export. It REPLACES the
	// event's pivot (site is the source of truth); local attach/detach edits are
	// replayed on top afterwards. Nil means the export carried no pivot (a
	// pre-v2 export) — the importer seeds it from member usage instead.
	CategoryRaces []CategoryRace
}

// ApplyEventImport writes a parsed export in a single transaction: either every
// site entity lands or none do, so a malformed or internally-inconsistent
// export can never leave a half-updated .chrono before a race. Parents are
// written before children (event → laps → races → categories → checkpoints →
// members → logs) to satisfy foreign keys. The local-edits replay runs after
// this commits (it touches only the now-consistent imported rows).
func (s *Store) ApplyEventImport(ctx context.Context, d EventImportData) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin import: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	if err := upsertEvent(ctx, tx, d.Event); err != nil {
		return err
	}
	for _, l := range d.Laps {
		if err := upsertLap(ctx, tx, l); err != nil {
			return err
		}
	}
	for _, r := range d.Races {
		if err := upsertRace(ctx, tx, r); err != nil {
			return err
		}
	}
	for _, c := range d.Categories {
		if err := upsertCategory(ctx, tx, c); err != nil {
			return err
		}
	}
	// Replace the event's race↔category pivot with the export's: the site owns
	// it, and local attach/detach edits replay on top after the commit. Done
	// here (after races+categories upsert) so the FK targets exist.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM race_categories WHERE race_id IN (SELECT id FROM races WHERE event_id = ?)`,
		d.Event.ID); err != nil {
		return fmt.Errorf("clear race_categories: %w", err)
	}
	for _, cr := range d.CategoryRaces {
		if err := upsertRaceCategory(ctx, tx, cr.RaceID, cr.CategoryID); err != nil {
			return err
		}
	}
	for _, cp := range d.Checkpoints {
		if err := upsertCheckpoint(ctx, tx, cp); err != nil {
			return err
		}
	}
	for _, m := range d.Members {
		if err := upsertMember(ctx, tx, m); err != nil {
			return err
		}
	}
	if err := upsertRfidLogs(ctx, tx, d.RfidLogs); err != nil {
		return err
	}
	return tx.Commit()
}
