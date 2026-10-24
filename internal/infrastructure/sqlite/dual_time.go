package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	timing "gitlab.com/fightmaster1/timing-core"
)

func defaultTimingBasis(basis timing.TimingBasis) string {
	if basis == "" {
		return "legacy"
	}
	return string(basis)
}

func importedStartSource(member domain.Member) string {
	if !member.StartProvenancePresent {
		return "unknown"
	}
	switch member.StartTimeSource {
	case timing.StartTimeUnknown, timing.StartTimeManual, timing.StartTimeRaceDefault, timing.StartTimeObservation:
		return string(member.StartTimeSource)
	default:
		return "unknown"
	}
}

func nullableObservation(id string) any {
	if id == "" {
		return nil
	}
	return id
}

func addAssignedStartDualTime(db *sql.DB) error {
	changed := false
	for _, item := range []struct{ table, name, kind string }{
		{"races", "timing_basis", "TEXT NOT NULL DEFAULT 'legacy'"},
		{"races", "shift_assigned_starts", "INTEGER NOT NULL DEFAULT 0"},
		{"members", "chip_start_time_ms", "INTEGER"},
		{"members", "chip_start_source", "TEXT NOT NULL DEFAULT ''"},
		{"members", "chip_start_observation_id", "TEXT"},
	} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('"+item.table+"') WHERE name=?", item.name).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			if _, err := db.Exec("ALTER TABLE " + item.table + " ADD COLUMN " + item.name + " " + item.kind); err != nil {
				return err
			}
			changed = true
		}
	}
	if changed {
		_, err := db.Exec("DROP TRIGGER IF EXISTS projection_revision_v1_races_update")
		return err
	}
	return nil
}

// PrepareDualTiming classifies only under an explicit mode-change command.
// Every assigned value is journaled so reimport/sync retains the same decision.
func (s *Store) PrepareDualTiming(ctx context.Context, raceID string, confirmUnknown bool) error {
	return s.PrepareAssignedStarts(ctx, raceID, confirmUnknown, true)
}

func (s *Store) PrepareAssignedStarts(ctx context.Context, raceID string, confirmUnknown, rebuildMeasured bool) error {
	if s.tx == nil {
		return fmt.Errorf("timing policy requires a transaction")
	}
	race, err := s.GetRace(ctx, raceID)
	if err != nil {
		return err
	}
	if race.TimingBasis.Dual() {
		return nil
	}
	members, err := s.ListMembersByRace(ctx, raceID)
	if err != nil {
		return err
	}
	for _, m := range members {
		if m.StartTimeMs != nil && m.StartTimeSource != timing.StartTimeManual && m.StartTimeSource != timing.StartTimeObservation && m.StartTimeSource != timing.StartTimeRaceDefault && !confirmUnknown {
			return fmt.Errorf("подтвердите сохранённые личные старты как назначенные перед включением gun/chip")
		}
	}
	for _, m := range members {
		if m.StartTimeMs == nil || !rebuildMeasured && (m.StartTimeSource == timing.StartTimeObservation || m.StartTimeSource == timing.StartTimeRaceDefault) {
			continue
		}
		assigned := m.StartTimeMs
		if m.StartTimeSource == timing.StartTimeObservation || m.StartTimeSource == timing.StartTimeRaceDefault {
			assigned = nil
		}
		if _, err := s.db.ExecContext(ctx, "UPDATE members SET start_time_ms=?,start_time_source=?,start_observation_id=NULL WHERE id=?", assigned, func() string {
			if assigned == nil {
				return "unknown"
			}
			return "manual"
		}(), m.ID); err != nil {
			return err
		}
		before, _ := json.Marshal(m.StartTimeMs)
		after, _ := json.Marshal(assigned)
		if err := s.InsertLocalChange(ctx, LocalChange{Entity: "member", EntityID: m.ID, Field: "start_time_ms", OldValue: string(before), NewValue: string(after), ChangedAt: time.Now().UnixMilli()}); err != nil {
			return err
		}
	}
	s.finishChanged()
	return nil
}
