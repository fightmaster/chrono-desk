package sqlite

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPacketRelayCredentialSurvivesLostResponseOutsideEventBackups(t *testing.T) {
	dir := t.TempDir()
	catalog, err := NewEventCatalog(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	ctx := context.Background()
	first, err := catalog.PreparePacketRelay(ctx, "621632", "https://app.chrono.events")
	if err != nil {
		t.Fatal(err)
	}
	retry, err := catalog.PreparePacketRelay(ctx, "621632", "https://app.chrono.events")
	if err != nil {
		t.Fatal(err)
	}
	if first.Credential == "" || first.Credential != retry.Credential || first.DeskInstanceID != catalog.InstallationID() {
		t.Fatalf("lost-response identity changed: first=%+v retry=%+v", first, retry)
	}
	encoded, _ := json.Marshal(first)
	if string(encoded) == "" || contains(string(encoded), first.Credential) {
		t.Fatalf("credential leaked through status JSON: %s", encoded)
	}
	first.RelayID = "66666666-6666-4666-8666-666666666666"
	first.APIBaseURL = "https://app.chrono.events/api/packet-issuance/v1/relays/" + first.RelayID
	first.ScopeID = "site:authority:621632"
	first.ExpiresAt = "2026-10-13T08:00:00.000Z"
	if err := catalog.CompletePacketRelay(ctx, first); err != nil {
		t.Fatal(err)
	}
	stored, found, err := catalog.GetPacketRelay(ctx, "621632")
	if err != nil || !found || stored.RelayID != first.RelayID || stored.Credential != first.Credential {
		t.Fatalf("stored relay=%+v found=%t err=%v", stored, found, err)
	}
	info, err := os.Stat(filepath.Join(dir, packetRelayStateFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("relay state permissions=%v err=%v", info.Mode().Perm(), err)
	}
	ids, err := catalog.ListEventIDs()
	if err != nil || len(ids) != 0 {
		t.Fatalf("relay state appeared as an event backup: ids=%v err=%v", ids, err)
	}
}

func TestPacketRelaySiteChangeRotatesOnlyThePreparedCredential(t *testing.T) {
	catalog, err := NewEventCatalog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.Close() })
	ctx := context.Background()
	first, _ := catalog.PreparePacketRelay(ctx, "621632", "https://one.example")
	second, err := catalog.PreparePacketRelay(ctx, "621632", "https://two.example")
	if err != nil {
		t.Fatal(err)
	}
	if first.Credential == second.Credential || second.SiteBaseURL != "https://two.example" || second.RelayID != "" {
		t.Fatalf("site replacement did not create an unclaimed identity: %+v", second)
	}
}

func contains(value, part string) bool {
	for i := 0; i+len(part) <= len(value); i++ {
		if value[i:i+len(part)] == part {
			return true
		}
	}
	return false
}
