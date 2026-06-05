package sqlite

import (
	"context"
	"fmt"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

// Entity upserts used by the event importer. All keyed on run5 string ids so
// re-imports overwrite site-owned data in place.

func (s *Store) UpsertLap(ctx context.Context, l domain.Lap) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO laps (id, name, slug, description) VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, slug=excluded.slug, description=excluded.description`,
		l.ID, l.Name, l.Slug, l.Description)
	if err != nil {
		return fmt.Errorf("upsert lap %s: %w", l.ID, err)
	}
	return nil
}

func (s *Store) UpsertRace(ctx context.Context, r domain.Race) error {
	_, err := s.db.ExecContext(ctx, `
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
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO categories (id, name, min, max, gender) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, min=excluded.min, max=excluded.max, gender=excluded.gender`,
		c.ID, c.Name, c.Min, c.Max, c.Gender)
	if err != nil {
		return fmt.Errorf("upsert category %s: %w", c.ID, err)
	}
	return nil
}

func (s *Store) UpsertCheckpoint(ctx context.Context, cp domain.Checkpoint) error {
	_, err := s.db.ExecContext(ctx, `
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

func (s *Store) UpsertMember(ctx context.Context, m domain.Member) error {
	_, err := s.db.ExecContext(ctx, `
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

// UpsertRfidLogs is the event-import variant: unlike InsertRfidLogs it also
// refreshes disabled_at on existing rows, so a re-export that disables a log
// (run5 ADR-0007) takes effect on the next recount.
func (s *Store) UpsertRfidLogs(ctx context.Context, logs []domain.RfidLog) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO rfid_logs (id, event_id, status, number, time_ms, ant, epc, rssi, board, disabled_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET disabled_at=excluded.disabled_at`)
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
	return tx.Commit()
}
