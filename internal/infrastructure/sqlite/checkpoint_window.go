package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	timing "gitlab.com/fightmaster1/timing-core"
	projection "gitlab.com/fightmaster1/timing-core/eventprojection"
)

func defaultReadSelection(value string) string {
	if value == "" {
		return "first"
	}
	return value
}

func validateCheckpointWindow(cp domain.Checkpoint) error {
	err := timing.ValidateCheckpointWindow(timing.Checkpoint[string]{ID: cp.ID, Type: timing.CheckpointType(cp.Type), SinceMs: cp.SinceMs, SinceOffsetSeconds: cp.SinceOffsetSeconds, UntilMs: cp.UntilMs, UntilOffsetSeconds: cp.UntilOffsetSeconds, ReadSelection: timing.ReadSelection(cp.ReadSelection)})
	if err != nil {
		return fmt.Errorf("некорректное окно чекпоинта: для последнего чтения нужна верхняя граница; верхняя граница не должна быть раньше нижней")
	}
	return nil
}

func addCheckpointWindows(db *sql.DB) error {
	changed := false
	for _, column := range []struct{ name, kind string }{{"until_ms", "INTEGER"}, {"until_offset_seconds", "INTEGER"}, {"read_selection", "TEXT NOT NULL DEFAULT 'first'"}} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('checkpoints') WHERE name=?", column.name).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			if _, err := db.Exec("ALTER TABLE checkpoints ADD COLUMN " + column.name + " " + column.kind); err != nil {
				return err
			}
			changed = true
		}
	}
	if changed {
		_, err := db.Exec("DROP TRIGGER IF EXISTS projection_revision_v1_checkpoints_update")
		return err
	}
	return nil
}

func (s *Store) HasWindowCheckpoint(ctx context.Context, eventID string) (bool, error) {
	var found bool
	err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM checkpoints WHERE event_id=? AND (read_selection='last' OR until_ms IS NOT NULL OR until_offset_seconds IS NOT NULL OR (type=1 AND since_offset_seconds IS NOT NULL)))", eventID).Scan(&found)
	return found, err
}

// ReplayWindowMember deliberately uses persisted complete member history. A
// later read can replace a bounded last selection and invalidate following slots.
func (repo *ProcessorRepo) ReplayWindowMember(ctx context.Context, log domain.RfidLog, raceFilter string) (bool, error) {
	enabled, err := repo.store.HasWindowCheckpoint(ctx, log.EventID)
	if err != nil || !enabled {
		return false, err
	}
	err = repo.store.WithinTx(ctx, func(tx *Store) error {
		member, found, err := NewProcessorRepo(tx).LoadMember(ctx, log.EventID, log)
		if err != nil || !found {
			return err
		}
		if raceFilter != "" && member.RaceID != raceFilter {
			return nil
		}
		return tx.ReplayWindowMembers(ctx, log.EventID, []string{member.ID})
	})
	return true, err
}

func (s *Store) ReplayWindowMembers(ctx context.Context, eventID string, ids []string) error {
	if s.tx == nil {
		return fmt.Errorf("member window replay requires transaction")
	}
	input, err := s.LoadTimingSnapshot(ctx, eventID)
	if err != nil {
		return err
	}
	logs, err := s.ListRfidLogsForMembers(ctx, eventID, ids)
	if err != nil {
		return err
	}
	for _, log := range logs {
		input.Readings = append(input.Readings, projection.Reading[string]{EventID: log.EventID, Disabled: log.DisabledAt != nil, Observation: timing.Observation{ID: log.ID, TimeMs: log.TimeMs, Number: log.Number, EPC: log.EPC, Board: log.Board}})
	}
	result, err := projection.Compute(ctx, input)
	if err != nil {
		return err
	}
	return s.replaceTimingProjection(ctx, eventID, "", ids, result)
}

func validateCheckpointRowWindow(cp CheckpointRow) error {
	return validateCheckpointWindow(domain.Checkpoint{ID: cp.ID, Type: domain.CheckpointType(cp.Type), SinceMs: cp.SinceMs, SinceOffsetSeconds: cp.SinceOffsetSeconds, UntilMs: cp.UntilMs, UntilOffsetSeconds: cp.UntilOffsetSeconds, ReadSelection: cp.ReadSelection})
}
