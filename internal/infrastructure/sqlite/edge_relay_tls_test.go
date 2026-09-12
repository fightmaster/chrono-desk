package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestEdgeRelayTLSConfigMigrationAndRevisionPreservePendingPacket(t *testing.T) {
	ctx := context.Background()
	store, previous, now := relayStoreFixture(t)
	before, _ := store.EdgeJournal(ctx, "100", 0, 10)
	// Recreate only the old test-fixture configuration shape, then exercise the
	// actual forward-only migration. The pending source journal remains intact.
	if _, err := store.DB().Exec(`ALTER TABLE edge_relay_config DROP COLUMN tls_bundle`); err != nil {
		t.Fatal(err)
	}
	if err := addEdgeRelayDelivery(store.DB()); err != nil {
		t.Fatal(err)
	}
	if restored, err := store.EdgeRelayConfig(ctx, "100"); err != nil || restored != previous {
		t.Fatalf("legacy relay changed on migration: %+v %v", restored, err)
	}
	claim, ok, err := store.ClaimEdgeRelay(ctx, "100", previous, now)
	if err != nil || !ok {
		t.Fatal("missing pending relay claim")
	}
	next := previous
	next.Endpoint, next.TLSBundle = "tls://hub.test:44004", filepath.Join(t.TempDir(), "desk-only")
	if _, err := store.ConfigureEdgeRelay(ctx, "100", next, false); err == nil {
		t.Fatal("TLS cutover redirected a pending queue without consent")
	}
	saved, err := store.ConfigureEdgeRelay(ctx, "100", next, true)
	if err != nil || saved.Revision != previous.Revision+1 {
		t.Fatalf("TLS configuration not revision fenced: %+v %v", saved, err)
	}
	if err := store.FinishEdgeRelay(ctx, "100", claim, now, nil, now); err == nil {
		t.Fatal("old plain sender ACK completed a TLS revision")
	}
	claim, ok, err = store.ClaimEdgeRelay(ctx, "100", saved, now.Add(31*time.Second))
	if err != nil || !ok || string(claim.Payload) != string(before[0].Payload) {
		t.Fatal("TLS cutover changed or lost the immutable source packet")
	}
	for _, candidate := range []struct{ endpoint, bundle string }{
		{"tls://hub.test:44004", ""}, {"hub.test:44004", saved.TLSBundle},
		{"tls://hub.test:44004", "relative"}, {"tls://", saved.TLSBundle},
	} {
		invalid := saved
		invalid.Endpoint, invalid.TLSBundle = candidate.endpoint, candidate.bundle
		if _, err := store.ConfigureEdgeRelay(ctx, "100", invalid, true); err == nil {
			t.Fatal("incomplete or downgraded TLS configuration accepted")
		}
	}
	if current, _ := store.EdgeRelayConfig(ctx, "100"); current != saved {
		t.Fatal("failed TLS config changed persisted admission")
	}
}
