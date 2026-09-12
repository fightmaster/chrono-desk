package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const packetRelayStateFile = ".packet-issuance-relays.sqlite"

type PacketRelayState struct {
	EventID        string `json:"event_id"`
	SiteBaseURL    string `json:"site_base_url"`
	DeskInstanceID string `json:"desk_instance_id"`
	Credential     string `json:"-"`
	RelayID        string `json:"relay_id"`
	APIBaseURL     string `json:"api_base_url"`
	ScopeID        string `json:"scope_id"`
	ExpiresAt      string `json:"expires_at"`
	EnrolledAt     *int64 `json:"enrolled_at"`
}

type packetRelayState struct {
	db *sql.DB
	mu sync.Mutex
}

func loadOrCreatePacketRelayState(dataDir string) (*packetRelayState, error) {
	path := filepath.Join(dataDir, packetRelayStateFile)
	db, err := Open(path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("protect packet relay state: %w", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS packet_relays (
			event_id TEXT PRIMARY KEY,
			site_base_url TEXT NOT NULL,
			desk_instance_id TEXT NOT NULL,
			credential TEXT NOT NULL,
			relay_id TEXT NOT NULL DEFAULT '',
			api_base_url TEXT NOT NULL DEFAULT '',
			scope_id TEXT NOT NULL DEFAULT '',
			expires_at TEXT NOT NULL DEFAULT '',
			enrolled_at INTEGER
		)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create packet relay state: %w", err)
	}
	return &packetRelayState{db: db}, nil
}

func (s *packetRelayState) Prepare(ctx context.Context, eventID, siteBaseURL, deskInstanceID string) (PacketRelayState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if eventID == "" || siteBaseURL == "" || deskInstanceID == "" {
		return PacketRelayState{}, fmt.Errorf("invalid packet relay identity")
	}
	existing, found, err := s.Get(ctx, eventID)
	if err != nil {
		return PacketRelayState{}, err
	}
	if found && existing.SiteBaseURL == siteBaseURL && existing.DeskInstanceID == deskInstanceID {
		return existing, nil
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return PacketRelayState{}, fmt.Errorf("generate packet relay credential: %w", err)
	}
	state := PacketRelayState{
		EventID: eventID, SiteBaseURL: siteBaseURL, DeskInstanceID: deskInstanceID,
		Credential: base64.RawURLEncoding.EncodeToString(secret),
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO packet_relays
		(event_id,site_base_url,desk_instance_id,credential)
		VALUES(?,?,?,?) ON CONFLICT(event_id) DO UPDATE SET
		site_base_url=excluded.site_base_url,desk_instance_id=excluded.desk_instance_id,
		credential=excluded.credential,relay_id='',api_base_url='',scope_id='',expires_at='',
		enrolled_at=NULL`, eventID, siteBaseURL, deskInstanceID, state.Credential)
	if err != nil {
		return PacketRelayState{}, fmt.Errorf("prepare packet relay: %w", err)
	}
	return state, nil
}

func (s *packetRelayState) Complete(ctx context.Context, state PacketRelayState) error {
	now := time.Now().UnixMilli()
	result, err := s.db.ExecContext(ctx, `UPDATE packet_relays SET
		relay_id=?,api_base_url=?,scope_id=?,expires_at=?,enrolled_at=?
		WHERE event_id=? AND site_base_url=? AND desk_instance_id=? AND credential=?`,
		state.RelayID, state.APIBaseURL, state.ScopeID, state.ExpiresAt, now,
		state.EventID, state.SiteBaseURL, state.DeskInstanceID, state.Credential)
	if err != nil {
		return fmt.Errorf("complete packet relay: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return fmt.Errorf("packet relay preparation changed")
	}
	return nil
}

func (s *packetRelayState) Get(ctx context.Context, eventID string) (PacketRelayState, bool, error) {
	var state PacketRelayState
	err := s.db.QueryRowContext(ctx, `SELECT event_id,site_base_url,desk_instance_id,credential,
		relay_id,api_base_url,scope_id,expires_at,enrolled_at
		FROM packet_relays WHERE event_id=?`, eventID).Scan(
		&state.EventID, &state.SiteBaseURL, &state.DeskInstanceID, &state.Credential,
		&state.RelayID, &state.APIBaseURL, &state.ScopeID, &state.ExpiresAt, &state.EnrolledAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return PacketRelayState{}, false, nil
	}
	if err != nil {
		return PacketRelayState{}, false, fmt.Errorf("get packet relay: %w", err)
	}
	return state, true, nil
}

func (s *packetRelayState) Close() error { return s.db.Close() }
