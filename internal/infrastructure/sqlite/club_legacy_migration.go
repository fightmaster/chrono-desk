package sqlite

import (
	"context"
	"database/sql"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

// Capture once: later Team edits or modern explicit Club clears never become a fallback.
func migrateLegacyClubAffiliation(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS club_metadata_versions(name TEXT PRIMARY KEY);
 CREATE TABLE IF NOT EXISTS member_club_legacy_values(event_id TEXT NOT NULL,member_id TEXT PRIMARY KEY,legacy_team TEXT NOT NULL,decision TEXT NOT NULL,captured_at INTEGER NOT NULL);`); err != nil {
		return err
	}
	var done int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM club_metadata_versions WHERE name='legacy-team-v2'`).Scan(&done); err != nil {
		return err
	}
	if done != 0 {
		return tx.Commit()
	}
	if _, err = tx.Exec(`INSERT INTO member_club_legacy_values(event_id,member_id,legacy_team,decision,captured_at)
 SELECT m.event_id,m.id,m.team,'pending',? FROM members m
 WHERE m.team IS NOT NULL AND TRIM(m.team)<>'' AND NOT EXISTS(SELECT 1 FROM member_club_metadata c WHERE c.member_id=m.id AND c.event_id=m.event_id)`, time.Now().UnixMilli()); err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT event_id,member_id,legacy_team FROM member_club_legacy_values WHERE decision='pending' ORDER BY member_id`)
	if err != nil {
		return err
	}
	type candidate struct{ eventID, memberID, team string }
	var candidates []candidate
	for rows.Next() {
		var row candidate
		if err = rows.Scan(&row.eventID, &row.memberID, &row.team); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, row := range candidates {
		club, validationErr := domain.NormalizeClub(&row.team)
		decision := "applied"
		if validationErr != nil {
			decision = "invalid"
		} else if _, err = applyMemberClubs(context.Background(), tx, row.eventID, map[string]*string{row.memberID: club}); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE member_club_legacy_values SET decision=? WHERE member_id=?`, decision, row.memberID); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`INSERT INTO club_metadata_versions(name) VALUES('legacy-team-v2')`); err != nil {
		return err
	}
	return tx.Commit()
}

// Only newly introduced legacy registrations may seed Club from Team.
func importedMemberClubs(ctx context.Context, tx *sql.Tx, d EventImportData) (map[string]*string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM members WHERE event_id=?`, d.Event.ID)
	if err != nil {
		return nil, err
	}
	existing := map[string]bool{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		existing[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	clubs := make(map[string]*string, len(d.Clubs))
	for id, club := range d.Clubs {
		clubs[id] = club
	}
	for _, member := range d.Members {
		if _, provided := clubs[member.ID]; !provided && !existing[member.ID] && member.Team != nil {
			club, err := domain.NormalizeClub(member.Team)
			if err != nil {
				return nil, err
			}
			clubs[member.ID] = club
		}
	}
	return filterClubOwners(ctx, tx, d.Event.ID, clubs, d.ClubOwners)
}
