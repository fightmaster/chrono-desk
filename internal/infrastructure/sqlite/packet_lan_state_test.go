package sqlite

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestPacketLANConnectionStoresOnlyHashesAndRevokesIndividually(t *testing.T) {
	state, err := loadOrCreatePacketRelayState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	ctx := context.Background()
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	invitation, err := state.CreateLANInvitation(ctx, "621632", "site:authority:621632", "Стол 1", now)
	if err != nil {
		t.Fatal(err)
	}
	credential := strings.Repeat("A", 43)
	origin := "22222222-2222-4222-8222-222222222222"
	claimed, err := state.ClaimLAN(ctx, invitation.ConnectionID, invitation.PairingCode, origin, credential, now)
	if err != nil || claimed.OriginInstanceID != origin || claimed.ClaimedAt == nil {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	if _, err := state.ClaimLAN(ctx, invitation.ConnectionID, invitation.PairingCode, origin, credential, now.Add(time.Second)); err != nil {
		t.Fatalf("lost claim acknowledgement was not retryable: %v", err)
	}
	if _, err := state.ClaimLAN(ctx, invitation.ConnectionID, invitation.PairingCode,
		"33333333-3333-4333-8333-333333333333", credential, now); err == nil {
		t.Fatal("claimed connection changed origin")
	}
	var pairingHash, credentialHash string
	if err := state.db.QueryRow(`SELECT pairing_hash,credential_hash FROM packet_lan_connections WHERE connection_id=?`, invitation.ConnectionID).
		Scan(&pairingHash, &credentialHash); err != nil {
		t.Fatal(err)
	}
	if pairingHash == invitation.PairingCode || credentialHash == credential || len(pairingHash) != 64 || len(credentialHash) != 64 {
		t.Fatal("LAN credential was stored in recoverable form")
	}
	if _, err := state.AuthenticateLAN(ctx, invitation.ConnectionID, credential, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := state.RevokeLAN(ctx, "621632", invitation.ConnectionID, "Тестовый отзыв", now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := state.AuthenticateLAN(ctx, invitation.ConnectionID, credential, now.Add(2*time.Hour)); err == nil {
		t.Fatal("revoked LAN connection was admitted")
	}
	var auditCount int
	if err := state.db.QueryRow(`SELECT COUNT(*) FROM packet_lan_audit WHERE connection_id=?`, invitation.ConnectionID).Scan(&auditCount); err != nil || auditCount != 3 {
		t.Fatalf("audit=%d err=%v", auditCount, err)
	}
}

func TestPacketLANInvitationLimitsLivePeersAndExpiry(t *testing.T) {
	state, err := loadOrCreatePacketRelayState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	ctx := context.Background()
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	first, err := state.CreateLANInvitation(ctx, "621632", "site:authority:621632", "Стол 1", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.ClaimLAN(ctx, first.ConnectionID, first.PairingCode,
		"22222222-2222-4222-8222-222222222222", strings.Repeat("A", 43), now.Add(11*time.Minute)); err == nil {
		t.Fatal("expired invitation was claimed")
	}
	for index := 1; index < packetLANMaxPeers; index++ {
		if _, err := state.CreateLANInvitation(ctx, "621632", "site:authority:621632", "Стол", now); err != nil {
			t.Fatalf("invitation %d: %v", index+1, err)
		}
	}
	if _, err := state.CreateLANInvitation(ctx, "621632", "site:authority:621632", "Лишний", now); err == nil {
		t.Fatal("live LAN peer limit was not enforced")
	}
	if _, err := state.CreateLANInvitation(ctx, "621632", "site:authority:621632", "После истечения", now.Add(11*time.Minute)); err != nil {
		t.Fatalf("expired invitations still counted as live: %v", err)
	}
}
