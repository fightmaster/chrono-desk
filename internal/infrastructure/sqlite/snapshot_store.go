package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	timing "gitlab.com/fightmaster1/timing-core"
	projection "gitlab.com/fightmaster1/timing-core/eventprojection"
)

// ErrSnapshotUnsupported leaves unsupported profiles on the existing explicit
// replay path. It must never hide a cursor, integrity or materialization error.
var ErrSnapshotUnsupported = errors.New("event is outside the fixed-distance snapshot profile")

// LoadFixedDistanceSnapshot reads configuration and judge finishes; raw facts
// are supplied separately by StreamSnapshotReadings without retaining a slice.
func (s *Store) LoadFixedDistanceSnapshot(ctx context.Context, eventID string) (projection.Snapshot[string], error) {
	return s.loadTimingSnapshot(ctx, eventID, false)
}

func (s *Store) LoadTimingSnapshot(ctx context.Context, eventID string) (projection.Snapshot[string], error) {
	return s.loadTimingSnapshot(ctx, eventID, true)
}

func (s *Store) loadTimingSnapshot(ctx context.Context, eventID string, all bool) (projection.Snapshot[string], error) {
	input := projection.Snapshot[string]{EventID: eventID}
	if err := s.scanSnapshotRows(ctx, `SELECT id,format,started_at_ms,category_excludes_top_by_gender,time_limit_seconds FROM races WHERE event_id=? ORDER BY id`, func(rows *sql.Rows) error {
		var race projection.Race[string]
		var format string
		if err := rows.Scan(&race.ID, &format, &race.StartedAtMs, &race.ExcludeTopByGender, &race.TimeLimitSeconds); err != nil {
			return err
		}
		if !all && format != "FixedDistance" {
			return ErrSnapshotUnsupported
		}
		switch format {
		case "FixedDistance":
			race.Format = timing.FormatFixedDistance
		case "TimeLimited":
			race.Format = timing.FormatTimeLimited
		case "Run5Stopwatch":
			race.Format = timing.FormatRun5Stopwatch
		default:
			return ErrSnapshotUnsupported
		}
		input.Races = append(input.Races, race)
		return nil
	}, eventID); err != nil {
		return projection.Snapshot[string]{}, err
	}
	if err := s.scanSnapshotRows(ctx, `SELECT id,race_id,number,epc,status,gender,category_id,start_time_ms,start_time_source,start_observation_id FROM members WHERE event_id=? ORDER BY id`, func(rows *sql.Rows) error {
		var member projection.Member[string]
		var epc, startObservation sql.NullString
		var status int
		if err := rows.Scan(&member.Timing.ID, &member.Timing.RaceID, &member.Number, &epc, &status, &member.Gender, &member.CategoryID, &member.Timing.StartTimeMs, &member.Timing.StartTimeSource, &startObservation); err != nil {
			return err
		}
		statuses := [...]string{"ok", "dns", "dnf", "dq"}
		if status < 0 || status >= len(statuses) {
			return ErrSnapshotUnsupported
		}
		member.Status, member.EPC, member.Timing.StartObservationID = statuses[status], epc.String, startObservation.String
		input.Members = append(input.Members, member)
		return nil
	}, eventID); err != nil {
		return projection.Snapshot[string]{}, err
	}
	if err := s.scanSnapshotRows(ctx, `SELECT id,race_id,type,sort,board,since_ms,since_offset_seconds,sleep_after_prev_seconds,until_ms,until_offset_seconds,read_selection FROM checkpoints WHERE event_id=? ORDER BY race_id,sort,id`, func(rows *sql.Rows) error {
		var checkpoint projection.Checkpoint[string]
		if err := rows.Scan(&checkpoint.Rule.ID, &checkpoint.RaceID, &checkpoint.Rule.Type, &checkpoint.Rule.Sort, &checkpoint.Board, &checkpoint.Rule.SinceMs, &checkpoint.Rule.SinceOffsetSeconds, &checkpoint.Rule.SleepAfterPrevSeconds, &checkpoint.Rule.UntilMs, &checkpoint.Rule.UntilOffsetSeconds, &checkpoint.Rule.ReadSelection); err != nil {
			return err
		}
		input.Checkpoints = append(input.Checkpoints, checkpoint)
		return nil
	}, eventID); err != nil {
		return projection.Snapshot[string]{}, err
	}
	if err := s.scanSnapshotRows(ctx, `SELECT id,member_id,race_id,time_ms,checkpoint_id FROM results WHERE event_id=? AND rfid_log_id IS NULL ORDER BY time_ms,id`, func(rows *sql.Rows) error {
		var manual projection.ManualFinish[string]
		var checkpoint, memberID sql.NullString
		if err := rows.Scan(&manual.Fact.ID, &memberID, &manual.RaceID, &manual.Fact.TimeMs, &checkpoint); err != nil {
			return err
		}
		if (!all && (checkpoint.Valid || !memberID.Valid)) || manual.Fact.TimeMs <= 0 {
			return ErrSnapshotUnsupported
		}
		manual.MemberID = memberID.String
		if all && !memberID.Valid {
			input.Crossings = append(input.Crossings, projection.Crossing[string]{ID: manual.Fact.ID, RaceID: manual.RaceID, TimeMs: manual.Fact.TimeMs})
			return nil
		}
		if checkpoint.Valid {
			input.ManualPasses = append(input.ManualPasses, projection.ManualPass[string]{ID: manual.Fact.ID, MemberID: manual.MemberID, RaceID: manual.RaceID, CheckpointID: checkpoint.String, TimeMs: manual.Fact.TimeMs})
		} else {
			input.ManualFinishes = append(input.ManualFinishes, manual)
		}
		return nil
	}, eventID); err != nil {
		return projection.Snapshot[string]{}, err
	}
	return input, nil
}

func (s *Store) scanSnapshotRows(ctx context.Context, query string, scan func(*sql.Rows) error, args ...any) error {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *Store) StreamSnapshotReadings(ctx context.Context, eventID, raceID string, yield func(projection.Reading[string]) error) (int, error) {
	query := `SELECT l.id,l.event_id,l.number,l.time_ms,l.epc,l.board,l.disabled_at IS NOT NULL,0 FROM rfid_logs l WHERE l.event_id=? ORDER BY l.time_ms,l.id`
	args := []any{eventID}
	if raceID != "" {
		// Match scoped legacy ResultExists: a result outside the requested race
		// survives its wipe and reserves that raw observation. Do not fabricate
		// a disabled flag or publish over another race's existing result.
		query = `SELECT l.id,l.event_id,l.number,l.time_ms,l.epc,l.board,l.disabled_at IS NOT NULL,r.id IS NOT NULL
			FROM rfid_logs l LEFT JOIN results r ON r.rfid_log_id=l.id AND r.race_id!=?
			WHERE l.event_id=? ORDER BY l.time_ms,l.id`
		args = []any{raceID, eventID}
	}
	count := 0
	err := s.scanSnapshotRows(ctx, query, func(rows *sql.Rows) error {
		var reading projection.Reading[string]
		var reservedOutside bool
		if err := rows.Scan(&reading.Observation.ID, &reading.EventID, &reading.Observation.Number, &reading.Observation.TimeMs, &reading.Observation.EPC, &reading.Observation.Board, &reading.Disabled, &reservedOutside); err != nil {
			return err
		}
		count++
		if reservedOutside {
			return nil
		}
		return yield(reading)
	}, args...)
	return count, err
}

// ReplaceFixedDistanceProjection only writes derived data in the caller's
// transaction. Raw observations, manual rows and ownership journals are untouched.
func (s *Store) ReplaceFixedDistanceProjection(ctx context.Context, eventID, raceID string, result projection.Projection[string]) error {
	return s.replaceTimingProjection(ctx, eventID, raceID, nil, result)
}

func (s *Store) replaceTimingProjection(ctx context.Context, eventID, raceID string, ids []string, result projection.Projection[string]) error {
	if s.tx == nil {
		return errors.New("snapshot replacement requires an existing transaction")
	}
	if raceID != "" {
		passes := make([]projection.Pass[string], 0)
		members := make([]projection.MemberTiming[string], 0)
		for _, pass := range result.Passes {
			if pass.RaceID == raceID {
				passes = append(passes, pass)
			}
		}
		for _, member := range result.Members {
			if member.Member.RaceID == raceID {
				members = append(members, member)
			}
		}
		result.Passes, result.Members = passes, members
	}
	if len(ids) > 0 {
		selected := map[string]bool{}
		for _, id := range ids {
			selected[id] = true
		}
		passes := []projection.Pass[string]{}
		for _, p := range result.Passes {
			if selected[p.MemberID] {
				passes = append(passes, p)
			}
		}
		members := []projection.MemberTiming[string]{}
		for _, m := range result.Members {
			if selected[m.Member.ID] {
				members = append(members, m)
			}
		}
		result.Passes, result.Members = passes, members
		if err := s.WipeDerivedResultsForMembers(ctx, eventID, ids); err != nil {
			return err
		}
	} else if err := s.WipeDerivedResults(ctx, eventID, raceID); err != nil {
		return err
	}
	rawPasses := make([]projection.Pass[string], 0, len(result.Passes))
	for _, pass := range result.Passes {
		if pass.ObservationID != "" {
			rawPasses = append(rawPasses, pass)
		}
	}
	result.Passes = rawPasses
	// Stay below even the historical SQLite999 bind limit; no statement or SQL
	// read/write round trip is issued per observation or member.
	const batchSize = 128
	for start := 0; start < len(result.Passes); start += batchSize {
		end := min(start+batchSize, len(result.Passes))
		args := make([]any, 0, (end-start)*6)
		values := make([]string, 0, end-start)
		for _, pass := range result.Passes[start:end] {
			values = append(values, "(?,?,?,?,?,?)")
			args = append(args, eventID, pass.RaceID, pass.MemberID, pass.CheckpointID, pass.ObservationID, pass.TimeMs)
		}
		query := `WITH projected(event_id,race_id,member_id,checkpoint_id,rfid_log_id,time_ms) AS (VALUES ` + strings.Join(values, ",") + `)
			INSERT INTO results(event_id,race_id,member_id,checkpoint_id,rfid_log_id,time_ms,number)
			SELECT p.event_id,p.race_id,p.member_id,p.checkpoint_id,p.rfid_log_id,p.time_ms,m.number
			FROM projected p JOIN rfid_logs l ON l.id=p.rfid_log_id AND l.event_id=p.event_id
			JOIN members m ON m.id=p.member_id AND m.event_id=p.event_id`
		if err := s.execSnapshotBatch(ctx, query, end-start, args...); err != nil {
			return err
		}
	}
	for start := 0; start < len(result.Members); start += batchSize {
		end := min(start+batchSize, len(result.Members))
		args := make([]any, 0, (end-start)*7)
		values := make([]string, 0, end-start)
		for _, item := range result.Members[start:end] {
			member := item.Member
			var observation any
			if member.StartObservationID != "" {
				observation = member.StartObservationID
			}
			values = append(values, "(?,?,?,?,?,?,?)")
			args = append(args, member.ID, eventID, member.StartTimeMs, member.StartTimeSource, observation, member.FinishTimeMs, item.CleanTime)
		}
		query := `WITH projected(id,event_id,start_ms,source,observation,finish_ms,clean) AS (VALUES ` + strings.Join(values, ",") + `)
			UPDATE members SET start_time_ms=p.start_ms,start_time_source=p.source,start_observation_id=p.observation,finish_time_ms=p.finish_ms,clean_time=p.clean
			FROM projected p WHERE members.id=p.id AND members.event_id=p.event_id`
		if err := s.execSnapshotBatch(ctx, query, end-start, args...); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) execSnapshotBatch(ctx context.Context, query string, expected int, args ...any) error {
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != int64(expected) {
		return fmt.Errorf("snapshot batch affected %d rows, expected %d", changed, expected)
	}
	return nil
}
