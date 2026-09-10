package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

func relayStoreFixture(t *testing.T) (*Store, domain.EdgeRelayConfig, time.Time) {
	t.Helper()
	store, event := edgeStoreFixture(t)
	if _, err := acceptEdge(store, "100", event); err != nil {
		t.Fatal(err)
	}
	config, err := store.ConfigureEdgeRelay(context.Background(), "100", domain.EdgeRelayConfig{Endpoint: "127.0.0.1:4004", Enabled: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	return store, config, time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
}

func TestEdgeRelayConfigIsExplicitRevisionFencedAndAudited(t *testing.T) {
	store, event := edgeStoreFixture(t)
	ctx := context.Background()
	if _, err := acceptEdge(store, "100", event); err != nil {
		t.Fatal(err)
	}
	want := domain.EdgeRelayConfig{Endpoint: "tcp://127.0.0.1:4004", Enabled: true}
	if _, err := store.ConfigureEdgeRelay(ctx, "100", want, false); err == nil {
		t.Fatal("pending queue silently assigned to first target")
	}
	if current, err := store.EdgeRelayConfig(ctx, "100"); err != nil || current.Enabled || current.Revision != 0 {
		t.Fatalf("failed command changed config: %+v %v", current, err)
	}
	saved, err := store.ConfigureEdgeRelay(ctx, "100", want, true)
	if err != nil || saved.Revision != 1 || saved.Endpoint != "127.0.0.1:4004" {
		t.Fatalf("save: %+v %v", saved, err)
	}
	if _, err := store.ConfigureEdgeRelay(ctx, "100", want, true); err == nil {
		t.Fatal("stale revision accepted")
	}
	if again, err := store.ConfigureEdgeRelay(ctx, "100", saved, false); err != nil || again != saved {
		t.Fatalf("idempotent config: %+v %v", again, err)
	}
	var count int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM local_changes WHERE entity='edge_relay'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("audit count=%d %v", count, err)
	}
	if _, err := store.DB().Exec(`CREATE TRIGGER fail_relay_audit BEFORE INSERT ON local_changes WHEN NEW.entity='edge_relay' BEGIN SELECT RAISE(ABORT,'audit fault'); END`); err != nil {
		t.Fatal(err)
	}
	paused := saved
	paused.Enabled = false
	if _, err := store.ConfigureEdgeRelay(ctx, "100", paused, false); err == nil {
		t.Fatal("unaudited pause committed")
	}
	if current, _ := store.EdgeRelayConfig(ctx, "100"); current != saved {
		t.Fatal("audit failure did not roll back settings")
	}
}

func TestEdgeRelayLeaseRetryAndACKDoNotModifyRawOrSourceWire(t *testing.T) {
	store, config, now := relayStoreFixture(t)
	ctx := context.Background()
	journal, _ := store.EdgeJournal(ctx, "100", 0, 10)
	if _, err := store.DB().Exec(`UPDATE rfid_logs SET disabled_at=1234`); err != nil {
		t.Fatal(err)
	}
	before, _ := store.ListRfidLogs(ctx, "100")
	claim, found, err := store.ClaimEdgeRelay(ctx, "100", config, now)
	if err != nil || !found || claim.Attempts != 1 || string(claim.Payload) != string(journal[0].Payload) {
		t.Fatalf("claim: %+v %t %v", claim, found, err)
	}
	if _, found, err := store.ClaimEdgeRelay(ctx, "100", config, now); err != nil || found {
		t.Fatalf("parallel claimant: %t %v", found, err)
	}
	if err := store.FinishEdgeRelay(ctx, "100", claim, now, errors.New("connection lost"), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := store.ClaimEdgeRelay(ctx, "100", config, now); found {
		t.Fatal("retry ignored persisted backoff")
	}
	second, found, err := store.ClaimEdgeRelay(ctx, "100", config, now.Add(time.Second))
	if err != nil || !found || second.Token == claim.Token || second.Attempts != 2 {
		t.Fatalf("retry: %+v %t %v", second, found, err)
	}
	if err := store.FinishEdgeRelay(ctx, "100", claim, now, nil, now); err == nil {
		t.Fatal("old ACK advanced a new claim")
	}
	if err := store.FinishEdgeRelay(ctx, "100", second, now.Add(time.Second), nil, now); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishEdgeRelay(ctx, "100", claim, now, errors.New("old error"), now); err == nil {
		t.Fatal("old failure reopened acknowledged work")
	}
	after, _ := store.ListRfidLogs(ctx, "100")
	if len(before) != 1 || len(after) != 1 || before[0].ID != after[0].ID || after[0].DisabledAt == nil || *after[0].DisabledAt != 1234 || *before[0].EdgeMetadata != *after[0].EdgeMetadata || before[0].OriginInstanceID != after[0].OriginInstanceID {
		t.Fatal("delivery changed stored observation")
	}
	items, _ := store.EdgeJournal(ctx, "100", 0, 10)
	if items[0].State != "acked" || string(items[0].Payload) != string(journal[0].Payload) {
		t.Fatal("ACK changed source payload")
	}
	if progress, err := store.EdgeRelayProgress(ctx, "100"); err != nil || progress.Acked != 1 || progress.Pending != 0 || progress.Attempts != 2 || progress.LastError != "" {
		t.Fatalf("progress: %+v %v", progress, err)
	}
	if _, found, err := store.ClaimEdgeRelay(ctx, "100", config, now.Add(time.Hour)); err != nil || found {
		t.Fatalf("acknowledged work reclaimed: %t %v", found, err)
	}
}

func TestEdgeRelayConfigSwitchAndExpiredLeaseRejectStaleCompletion(t *testing.T) {
	store, config, now := relayStoreFixture(t)
	ctx := context.Background()
	first, _, _ := store.ClaimEdgeRelay(ctx, "100", config, now)
	next := config
	next.Endpoint = "127.0.0.1:4005"
	if _, err := store.ConfigureEdgeRelay(ctx, "100", next, false); err == nil {
		t.Fatal("unconfirmed pending redirect")
	}
	next, err := store.ConfigureEdgeRelay(ctx, "100", next, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishEdgeRelay(ctx, "100", first, now, nil, now); err == nil {
		t.Fatal("old target ACK advanced changed config")
	}
	if _, found, _ := store.ClaimEdgeRelay(ctx, "100", next, now); found {
		t.Fatal("new process ignored active lease")
	}
	second, found, err := store.ClaimEdgeRelay(ctx, "100", next, now.Add(31*time.Second))
	if err != nil || !found || second.Endpoint != next.Endpoint || string(second.Payload) != string(first.Payload) {
		t.Fatalf("recovery: %+v %t %v", second, found, err)
	}
	if err := store.FinishEdgeRelay(ctx, "100", second, now.Add(31*time.Second), nil, now); err != nil {
		t.Fatal(err)
	}
}

func TestEdgeRelayACKStorageFailureKeepsOriginalClaimRetryable(t *testing.T) {
	store, config, now := relayStoreFixture(t)
	ctx := context.Background()
	claim, _, _ := store.ClaimEdgeRelay(ctx, "100", config, now)
	if _, err := store.DB().Exec(`CREATE TRIGGER fail_edge_ack BEFORE UPDATE ON edge_observation_outbox WHEN NEW.state='acked' BEGIN SELECT RAISE(ABORT,'disk fault'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishEdgeRelay(ctx, "100", claim, now, nil, now); err == nil {
		t.Fatal("failed ACK persistence reported success")
	}
	items, _ := store.EdgeJournal(ctx, "100", 0, 10)
	if items[0].State != "sent" || string(items[0].Payload) != string(claim.Payload) {
		t.Fatal("failed ACK write changed source journal")
	}
	if _, err := store.DB().Exec(`DROP TRIGGER fail_edge_ack`); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishEdgeRelay(ctx, "100", claim, now, nil, now); err != nil {
		t.Fatal(err)
	}
}

func TestEdgeRelayUpgradePreservesExistingPendingEnvelope(t *testing.T) {
	store, event := edgeStoreFixture(t)
	ctx := context.Background()
	if _, err := acceptEdge(store, "100", event); err != nil {
		t.Fatal(err)
	}
	before, _ := store.EdgeJournal(ctx, "100", 0, 10)
	if _, err := store.DB().Exec(`DROP INDEX idx_edge_outbox_due; DROP TABLE edge_relay_config`); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"relay_endpoint", "attempts", "next_attempt_at", "last_attempt_at", "lease_token", "lease_until"} {
		if _, err := store.DB().Exec(`ALTER TABLE edge_observation_outbox DROP COLUMN ` + field); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := New(store.DB()); err != nil {
			t.Fatal(err)
		}
	}
	after, _ := store.EdgeJournal(ctx, "100", 0, 10)
	config, _ := store.EdgeRelayConfig(ctx, "100")
	if len(after) != 1 || after[0].State != "pending" || string(before[0].Payload) != string(after[0].Payload) || config.Enabled || config.Endpoint != "" {
		t.Fatal("upgrade changed source packet or invented a target")
	}
}
