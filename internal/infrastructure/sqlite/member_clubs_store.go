package sqlite

import (
	"context"
	"database/sql"
)

// Club is display metadata, not Team or canonical packet Person state.
func addMemberClubMetadata(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS member_club_metadata (
  event_id TEXT NOT NULL,member_id TEXT NOT NULL REFERENCES members(id) ON DELETE CASCADE,
  club TEXT,PRIMARY KEY(event_id,member_id));`)
	return err
}

// Missing map entries are never deletion; a present nil explicitly clears Club.
func applyMemberClubs(ctx context.Context, ex execer, eventID string, clubs map[string]*string) (int, error) {
	count := 0
	for memberID, club := range clubs {
		result, err := ex.ExecContext(ctx, `INSERT INTO member_club_metadata(event_id,member_id,club)
   SELECT event_id,id,? FROM members WHERE event_id=? AND id=?
   ON CONFLICT(event_id,member_id) DO UPDATE SET club=excluded.club
   WHERE member_club_metadata.club IS NOT excluded.club`, club, eventID, memberID)
		if err != nil {
			return count, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return count, err
		}
		count += int(changed)
	}
	return count, nil
}

// ApplyMemberClubs changes display metadata only, retaining all timing/profile facts.
func (s *Store) ApplyMemberClubs(ctx context.Context, eventID string, clubs map[string]*string) (int, error) {
	count := 0
	err := s.WithinTx(ctx, func(tx *Store) error {
		var err error
		count, err = applyMemberClubs(ctx, tx.db, eventID, clubs)
		return err
	})
	return count, err
}

// MemberClubs returns explicit metadata, including null clear markers.
func (s *Store) MemberClubs(ctx context.Context, eventID string) (map[string]*string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT member_id,club FROM member_club_metadata WHERE event_id=?`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	clubs := make(map[string]*string)
	for rows.Next() {
		var id string
		var club *string
		if err := rows.Scan(&id, &club); err != nil {
			return nil, err
		}
		clubs[id] = club
	}
	return clubs, rows.Err()
}
