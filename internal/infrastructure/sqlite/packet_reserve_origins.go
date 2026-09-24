package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"gitlab.com/fightmaster1/chrono-desk/internal/packetissuance"
)

func (s *Store) SavePacketReserveOrigins(ctx context.Context, eventID string, origins *[]packetissuance.ReserveOrigin) error {
	if origins == nil {
		return nil
	}
	// Keep prior provenance available while local pending edits survive a rebase.
	var previous string
	err := s.db.QueryRowContext(ctx, `SELECT origins_json FROM packet_issuance_reserve_origins WHERE event_id=?`, eventID).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var combined []packetissuance.ReserveOrigin
	if previous != "" {
		if err := json.Unmarshal([]byte(previous), &combined); err != nil {
			return err
		}
	}
	combined = append(combined, (*origins)...)
	unique := make(map[packetissuance.ReserveOrigin]bool)
	compact := make([]packetissuance.ReserveOrigin, 0, len(combined))
	for _, origin := range combined {
		if !unique[origin] {
			compact = append(compact, origin)
			unique[origin] = true
		}
	}
	raw, err := json.Marshal(compact)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO packet_issuance_reserve_origins(event_id,origins_json) VALUES (?,?)
        ON CONFLICT(event_id) DO UPDATE SET origins_json=excluded.origins_json`, eventID, raw)
	return err
}

func (s *Store) PacketReserveOriginsReady(ctx context.Context, eventID string) (bool, error) {
	var ready bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM packet_issuance_reserve_origins WHERE event_id=?)`, eventID).Scan(&ready)
	return ready, err
}

func (s *Store) PacketReserveOrigins(ctx context.Context, eventID string, current []packetissuance.Registration) (*[]packetissuance.ReserveOrigin, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT origins_json FROM packet_issuance_reserve_origins WHERE event_id=?`, eventID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var origins []packetissuance.ReserveOrigin
	if err := json.Unmarshal([]byte(raw), &origins); err != nil {
		return nil, err
	}
	// Both journals are durable even after the outgoing LAN feed is compacted.
	rows, err := s.db.QueryContext(ctx, `SELECT operation_json FROM packet_issuance_operations
        WHERE event_id=? AND outcome IN ('applied','equivalent') UNION ALL
        SELECT action_json FROM packet_issuance_site_feed_actions WHERE event_id=? AND application IN ('applied','observed')`, eventID, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var envelope struct {
			Changes []packetissuance.FeedChange `json:"changes"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			return nil, err
		}
		for _, change := range envelope.Changes {
			for _, row := range []*packetissuance.Registration{change.Before, change.After} {
				if row != nil && row.Reserve && row.Bib != "" {
					origins = append(origins, packetissuance.ReserveOrigin{RegistrationID: row.ID, Bib: row.Bib, EPC: row.EPC, RaceID: row.RaceID})
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	matched := packetissuance.MatchingReserveOrigins(current, origins)
	return &matched, nil
}
