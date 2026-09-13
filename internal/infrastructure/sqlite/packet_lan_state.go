package sqlite

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	packetLANInvitationTTL = 10 * time.Minute
	packetLANConnectionTTL = 7 * 24 * time.Hour
	packetLANMaxPeers      = 32
)

type PacketLANInvitation struct {
	ConnectionID string
	EventID      string
	ScopeID      string
	Label        string
	PairingCode  string
	ExpiresAt    time.Time
}

type PacketLANConnection struct {
	ConnectionID     string     `json:"connection_id"`
	EventID          string     `json:"event_id"`
	ScopeID          string     `json:"scope_id"`
	Label            string     `json:"label"`
	OriginInstanceID string     `json:"origin_instance_id,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	InvitationExpiry time.Time  `json:"invitation_expires_at"`
	ExpiresAt        time.Time  `json:"expires_at"`
	ClaimedAt        *time.Time `json:"claimed_at,omitempty"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	RevokeReason     string     `json:"revoke_reason,omitempty"`
}

func createPacketLANTables(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS packet_lan_runtime (
			singleton INTEGER PRIMARY KEY CHECK(singleton=1),
			event_id TEXT NOT NULL,
			enabled INTEGER NOT NULL CHECK(enabled IN (0,1))
		);
		CREATE TABLE IF NOT EXISTS packet_lan_connections (
			connection_id TEXT PRIMARY KEY,
			event_id TEXT NOT NULL,
			scope_id TEXT NOT NULL,
			label TEXT NOT NULL,
			pairing_hash TEXT NOT NULL,
			credential_hash TEXT,
			origin_instance_id TEXT,
			created_at INTEGER NOT NULL,
			invitation_expires_at INTEGER NOT NULL,
			connection_expires_at INTEGER NOT NULL,
			claimed_at INTEGER,
			revoked_at INTEGER,
			revoke_reason TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX IF NOT EXISTS idx_packet_lan_connections_event
			ON packet_lan_connections(event_id,connection_expires_at,revoked_at);
		CREATE TABLE IF NOT EXISTS packet_lan_audit (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			connection_id TEXT NOT NULL,
			event_id TEXT NOT NULL,
			action TEXT NOT NULL,
			origin_instance_id TEXT,
			reason TEXT NOT NULL DEFAULT '',
			recorded_at INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_packet_lan_audit_connection
			ON packet_lan_audit(connection_id,recorded_at,id)
	`)
	if err != nil {
		return fmt.Errorf("create packet LAN state: %w", err)
	}
	return nil
}

func (s *packetRelayState) EnableLAN(ctx context.Context, eventID string) error {
	if strings.TrimSpace(eventID) == "" {
		return errors.New("invalid packet LAN event")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO packet_lan_runtime(singleton,event_id,enabled)
		VALUES(1,?,1) ON CONFLICT(singleton) DO UPDATE SET event_id=excluded.event_id,enabled=1`, eventID)
	return err
}

func (s *packetRelayState) DisableLAN(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE packet_lan_runtime SET enabled=0 WHERE singleton=1`)
	return err
}

func (s *packetRelayState) ActiveLAN(ctx context.Context) (string, bool, error) {
	var eventID string
	var enabled bool
	err := s.db.QueryRowContext(ctx, `SELECT event_id,enabled FROM packet_lan_runtime WHERE singleton=1`).Scan(&eventID, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return eventID, enabled, err
}

func (s *packetRelayState) CreateLANInvitation(ctx context.Context, eventID, scopeID, label string, now time.Time) (PacketLANInvitation, error) {
	label = strings.TrimSpace(label)
	if eventID == "" || scopeID == "" || !validPacketLANText(label, 160) {
		return PacketLANInvitation{}, errors.New("invalid packet LAN invitation")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PacketLANInvitation{}, err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM packet_lan_connections
		WHERE event_id=? AND revoked_at IS NULL AND
		((claimed_at IS NOT NULL AND connection_expires_at>?) OR
		 (claimed_at IS NULL AND invitation_expires_at>?))`, eventID, now.UnixMilli(), now.UnixMilli()).Scan(&count); err != nil {
		return PacketLANInvitation{}, err
	}
	if count >= packetLANMaxPeers {
		return PacketLANInvitation{}, errors.New("maximum packet LAN connections reached")
	}
	connectionID, err := packetLANUUID()
	if err != nil {
		return PacketLANInvitation{}, err
	}
	pairing, err := packetLANSecret()
	if err != nil {
		return PacketLANInvitation{}, err
	}
	created := now.UTC().Truncate(time.Millisecond)
	invitationExpiry := created.Add(packetLANInvitationTTL)
	connectionExpiry := created.Add(packetLANConnectionTTL)
	if _, err := tx.ExecContext(ctx, `INSERT INTO packet_lan_connections
		(connection_id,event_id,scope_id,label,pairing_hash,created_at,invitation_expires_at,connection_expires_at)
		VALUES(?,?,?,?,?,?,?,?)`, connectionID, eventID, scopeID, label, packetLANHash(pairing), created.UnixMilli(),
		invitationExpiry.UnixMilli(), connectionExpiry.UnixMilli()); err != nil {
		return PacketLANInvitation{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO packet_lan_audit
		(connection_id,event_id,action,recorded_at) VALUES(?,?,?,?)`, connectionID, eventID, "invite", created.UnixMilli()); err != nil {
		return PacketLANInvitation{}, err
	}
	if err := tx.Commit(); err != nil {
		return PacketLANInvitation{}, err
	}
	return PacketLANInvitation{ConnectionID: connectionID, EventID: eventID, ScopeID: scopeID,
		Label: label, PairingCode: pairing, ExpiresAt: invitationExpiry}, nil
}

func (s *packetRelayState) ClaimLAN(ctx context.Context, connectionID, pairingCode, originInstanceID, credential string, now time.Time) (PacketLANConnection, error) {
	if !validPacketLANUUID(originInstanceID) || !validPacketLANSecret(pairingCode) || !validPacketLANSecret(credential) {
		return PacketLANConnection{}, errors.New("invalid packet LAN claim")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PacketLANConnection{}, err
	}
	defer tx.Rollback()
	record, pairingHash, credentialHash, err := scanPacketLANConnection(tx.QueryRowContext(ctx, packetLANConnectionSQL+` WHERE connection_id=?`, connectionID))
	if err != nil {
		return PacketLANConnection{}, errors.New("packet LAN connection unavailable")
	}
	if record.RevokedAt != nil || !record.ExpiresAt.After(now) || !samePacketLANHash(pairingHash, pairingCode) {
		return PacketLANConnection{}, errors.New("packet LAN connection unavailable")
	}
	if record.ClaimedAt != nil {
		if record.OriginInstanceID != originInstanceID || !samePacketLANHash(credentialHash.String, credential) {
			return PacketLANConnection{}, errors.New("packet LAN connection identity conflict")
		}
		return record, tx.Commit()
	}
	if !record.InvitationExpiry.After(now) {
		return PacketLANConnection{}, errors.New("packet LAN invitation expired")
	}
	when := now.UTC().Truncate(time.Millisecond)
	result, err := tx.ExecContext(ctx, `UPDATE packet_lan_connections SET credential_hash=?,origin_instance_id=?,claimed_at=?
		WHERE connection_id=? AND claimed_at IS NULL`, packetLANHash(credential), originInstanceID, when.UnixMilli(), connectionID)
	if err != nil {
		return PacketLANConnection{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return PacketLANConnection{}, errors.New("packet LAN connection changed")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO packet_lan_audit
		(connection_id,event_id,action,origin_instance_id,recorded_at) VALUES(?,?,?,?,?)`,
		connectionID, record.EventID, "claim", originInstanceID, when.UnixMilli()); err != nil {
		return PacketLANConnection{}, err
	}
	record.OriginInstanceID = originInstanceID
	record.ClaimedAt = &when
	return record, tx.Commit()
}

func (s *packetRelayState) AuthenticateLAN(ctx context.Context, connectionID, credential string, now time.Time) (PacketLANConnection, error) {
	if !validPacketLANSecret(credential) {
		return PacketLANConnection{}, errors.New("packet LAN connection unavailable")
	}
	record, _, credentialHash, err := scanPacketLANConnection(s.db.QueryRowContext(ctx, packetLANConnectionSQL+` WHERE connection_id=?`, connectionID))
	if err != nil || record.ClaimedAt == nil || record.RevokedAt != nil || !record.ExpiresAt.After(now) ||
		!samePacketLANHash(credentialHash.String, credential) {
		return PacketLANConnection{}, errors.New("packet LAN connection unavailable")
	}
	return record, nil
}

func (s *packetRelayState) ListLANConnections(ctx context.Context, eventID string) ([]PacketLANConnection, error) {
	rows, err := s.db.QueryContext(ctx, packetLANConnectionSQL+` WHERE event_id=? ORDER BY created_at DESC,connection_id LIMIT 100`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []PacketLANConnection{}
	for rows.Next() {
		record, _, _, err := scanPacketLANConnection(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func (s *packetRelayState) RevokeLAN(ctx context.Context, eventID, connectionID, reason string, now time.Time) error {
	reason = strings.TrimSpace(reason)
	if !validPacketLANText(reason, 500) {
		return errors.New("invalid packet LAN revoke reason")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	when := now.UTC().Truncate(time.Millisecond)
	result, err := tx.ExecContext(ctx, `UPDATE packet_lan_connections SET revoked_at=?,revoke_reason=?
		WHERE event_id=? AND connection_id=? AND revoked_at IS NULL`, when.UnixMilli(), reason, eventID, connectionID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("packet LAN connection unavailable")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO packet_lan_audit
		(connection_id,event_id,action,reason,recorded_at) VALUES(?,?,?,?,?)`, connectionID, eventID, "revoke", reason, when.UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

const packetLANConnectionSQL = `SELECT connection_id,event_id,scope_id,label,pairing_hash,credential_hash,
	origin_instance_id,created_at,invitation_expires_at,connection_expires_at,claimed_at,revoked_at,revoke_reason
	FROM packet_lan_connections`

type packetLANScanner interface{ Scan(...any) error }

func scanPacketLANConnection(row packetLANScanner) (PacketLANConnection, string, sql.NullString, error) {
	var record PacketLANConnection
	var pairingHash string
	var credentialHash, origin sql.NullString
	var created, invitationExpiry, expiry int64
	var claimed, revoked sql.NullInt64
	err := row.Scan(&record.ConnectionID, &record.EventID, &record.ScopeID, &record.Label, &pairingHash, &credentialHash,
		&origin, &created, &invitationExpiry, &expiry, &claimed, &revoked, &record.RevokeReason)
	if err != nil {
		return PacketLANConnection{}, "", sql.NullString{}, err
	}
	record.OriginInstanceID = origin.String
	record.CreatedAt = time.UnixMilli(created).UTC()
	record.InvitationExpiry = time.UnixMilli(invitationExpiry).UTC()
	record.ExpiresAt = time.UnixMilli(expiry).UTC()
	if claimed.Valid {
		value := time.UnixMilli(claimed.Int64).UTC()
		record.ClaimedAt = &value
	}
	if revoked.Valid {
		value := time.UnixMilli(revoked.Int64).UTC()
		record.RevokedAt = &value
	}
	return record, pairingHash, credentialHash, nil
}

func packetLANSecret() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func packetLANUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func packetLANHash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func samePacketLANHash(stored, secret string) bool {
	want, err := hex.DecodeString(stored)
	if err != nil {
		return false
	}
	got := sha256.Sum256([]byte(secret))
	return subtle.ConstantTimeCompare(want, got[:]) == 1
}

func validPacketLANSecret(value string) bool {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == value
}

func validPacketLANUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' || value[14] != '4' {
		return false
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	return err == nil && len(decoded) == 16 && decoded[8]&0xc0 == 0x80
}

func validPacketLANText(value string, limit int) bool {
	if value == "" || !utf8.ValidString(value) || len([]byte(value)) > limit {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || (r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u2069') {
			return false
		}
	}
	return true
}
