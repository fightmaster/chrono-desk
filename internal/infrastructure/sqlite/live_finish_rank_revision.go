package sqlite

import (
	"context"
	"database/sql"
)

// LiveFinishRankRevision ignores raw input traffic and tracks rank eligibility.
type LiveFinishRankRevision struct {
	Config      int64
	Eligibility int64
}

func addLiveFinishEligibilityRevision(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS live_finish_eligibility_revisions (
  event_id TEXT PRIMARY KEY, revision INTEGER NOT NULL DEFAULT 0);
 CREATE TRIGGER IF NOT EXISTS live_finish_clean_eligibility_v1
 AFTER UPDATE OF clean_time ON members
 WHEN (OLD.clean_time IS NULL) <> (NEW.clean_time IS NULL)
 BEGIN
  INSERT INTO live_finish_eligibility_revisions(event_id,revision) VALUES(NEW.event_id,1)
  ON CONFLICT(event_id) DO UPDATE SET revision=revision+1;
 END;`)
	return err
}

func (s *Store) LiveFinishRankRevision(ctx context.Context, eventID string) (LiveFinishRankRevision, error) {
	var revision LiveFinishRankRevision
	err := s.db.QueryRowContext(ctx, `SELECT
 COALESCE((SELECT config_revision FROM projection_revisions WHERE event_id=?),0),
 COALESCE((SELECT revision FROM live_finish_eligibility_revisions WHERE event_id=?),0)`, eventID, eventID).Scan(&revision.Config, &revision.Eligibility)
	return revision, err
}
