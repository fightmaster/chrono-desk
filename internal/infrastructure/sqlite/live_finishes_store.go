package sqlite

import (
	"context"
	"fmt"
)

// LiveFinish is a committed finish, not a raw reading or a ranked protocol row.
// MemberID is internal and must be replaced by an opaque key at the LAN boundary.
type LiveFinish struct {
	MemberID      string
	RaceID        string
	RaceName      string
	Number        *int64
	FirstName     string
	LastName      string
	Club          *string
	Team          *string
	City          *string
	FinishTime    int64
	CleanTime     string
	Gender        *string
	CategoryName  *string
	GenderPlace   *int
	CategoryPlace *int
}

// LatestFinishes performs bounded indexed reads without replay or ranking.
func (s *Store) LatestFinishes(ctx context.Context, eventID, raceID string) ([]LiveFinish, error) {
	query := `SELECT m.id,m.race_id,r.name,m.number,m.first_name,m.last_name,
		m.team,m.city,m.finish_time_ms,m.clean_time,m.gender,c.name,club.club
		FROM members m JOIN races r ON r.id=m.race_id AND r.event_id=m.event_id
		LEFT JOIN categories c ON c.id=m.category_id
        LEFT JOIN member_club_metadata club ON club.event_id=m.event_id AND club.member_id=m.id
        WHERE m.event_id=? AND m.status=0 AND m.finish_time_ms IS NOT NULL
		AND m.clean_time IS NOT NULL AND m.finish_time_ms>0 AND m.clean_time<>''
		AND r.format='FixedDistance'
		AND NOT EXISTS (SELECT 1 FROM packet_issuance_registrations p
			WHERE p.event_id=m.event_id AND p.registration_id=m.id AND p.deleted=0
			AND COALESCE(json_extract(p.value_json,'$.reserve'),0)=1)`
	args := []any{eventID}
	if raceID != "" {
		query += ` AND m.race_id=?`
		args = append(args, raceID)
	}
	query += ` ORDER BY m.finish_time_ms DESC,m.id DESC LIMIT 10`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("latest finishes: %w", err)
	}
	defer rows.Close()
	finishes := make([]LiveFinish, 0)
	for rows.Next() {
		var finish LiveFinish
		if err := rows.Scan(&finish.MemberID, &finish.RaceID, &finish.RaceName,
			&finish.Number, &finish.FirstName, &finish.LastName, &finish.Team,
			&finish.City, &finish.FinishTime, &finish.CleanTime, &finish.Gender, &finish.CategoryName, &finish.Club); err != nil {
			return nil, err
		}
		finishes = append(finishes, finish)
	}
	return finishes, rows.Err()
}
