package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// SyncConfig is the per-event run5 sync target (base URL + token) plus the last
// push result. One row per event database.
type SyncConfig struct {
	BaseURL         string  `json:"base_url"`
	Token           string  `json:"token"`
	LastSyncedAt    *int64  `json:"last_synced_at"`
	LastPayloadHash *string `json:"last_payload_hash"`
}

// GetSyncConfig returns the event's sync target; a zero value if unset.
func (s *Store) GetSyncConfig(ctx context.Context, eventID string) (SyncConfig, error) {
	var c SyncConfig
	err := s.db.QueryRowContext(ctx,
		`SELECT base_url, token, last_synced_at, last_payload_hash FROM sync_config WHERE event_id = ?`,
		eventID).Scan(&c.BaseURL, &c.Token, &c.LastSyncedAt, &c.LastPayloadHash)
	if errors.Is(err, sql.ErrNoRows) {
		return SyncConfig{}, nil
	}
	if err != nil {
		return SyncConfig{}, fmt.Errorf("get sync config %s: %w", eventID, err)
	}
	return c, nil
}

// SetSyncConfig upserts the base URL and token, preserving the last-sync fields.
func (s *Store) SetSyncConfig(ctx context.Context, eventID, baseURL, token string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sync_config (event_id, base_url, token) VALUES (?, ?, ?)
		ON CONFLICT(event_id) DO UPDATE SET base_url=excluded.base_url, token=excluded.token`,
		eventID, baseURL, token)
	if err != nil {
		return fmt.Errorf("set sync config %s: %w", eventID, err)
	}
	return nil
}

// SetSyncResult records a successful push (timestamp + payload hash) so a
// re-push of identical data can be skipped.
func (s *Store) SetSyncResult(ctx context.Context, eventID string, whenMs int64, payloadHash string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sync_config (event_id, last_synced_at, last_payload_hash) VALUES (?, ?, ?)
		ON CONFLICT(event_id) DO UPDATE SET last_synced_at=excluded.last_synced_at, last_payload_hash=excluded.last_payload_hash`,
		eventID, whenMs, payloadHash)
	if err != nil {
		return fmt.Errorf("set sync result %s: %w", eventID, err)
	}
	return nil
}
