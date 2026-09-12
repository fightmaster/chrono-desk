//go:build linux && edgeintegration && edgecentralintegration

package service

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/rfid-core/edge"
	"gitlab.com/fightmaster1/rfid-core/ingest"
	"gitlab.com/fightmaster1/rfid-core/tcp"
)

func TestEdgeMutualTLSActualSourceHubAndCentralRaw(t *testing.T) {
	security := newEdgeChainTLS(t)
	security.allowRelay = true
	hub := newEdgeChainHubTopology(t, 100, "Feibot:U659", "100", "none", false, security)
	central := newEdgeChainCentral(t, hub, "Feibot:U659", "100", "setup-raw")
	catalog, err := sqlite.NewEventCatalog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	events := NewEventService(catalog, log.New(io.Discard, "", 0))
	t.Cleanup(events.Close)
	store, err := catalog.OpenOrCreate("100")
	if err != nil {
		t.Fatal(err)
	}
	observation := seedEdgeLiveFixture(t, store)
	if _, err := store.DB().Exec(`DELETE FROM checkpoints`); err != nil {
		t.Fatal(err)
	}
	manager := NewLiveManager(log.New(io.Discard, "", 0))
	t.Cleanup(manager.StopAll)
	if err := manager.ConfigureEdge(t.Context(), store, "100", []domain.EdgeBinding{{Board: observation.Board}}); err != nil {
		t.Fatal(err)
	}
	port := edgeTestPort(t)
	if err := manager.StartCombined(store, "100", port); err != nil {
		t.Fatal(err)
	}
	source := newEdgeChainSource(t, "feibot", hub.endpoint, "127.0.0.1:"+port)
	data, err := os.ReadFile(source.config)
	if err != nil {
		t.Fatal(err)
	}
	config := strings.Replace(string(data), "endpoint = tcp://"+hub.endpoint, "endpoint = tls://"+hub.endpoint, 1)
	config = strings.Replace(config, "[destination.hub]\n", fmt.Sprintf("[destination.hub]\ntls_certificate_file = %s\ntls_key_file = %s\ntls_server_ca_file = %s\ntls_server_name = localhost\n", filepath.Join(security.clientDir, "expired-client.pem"), filepath.Join(security.clientDir, "client-key.pem"), filepath.Join(security.clientDir, "ca.pem")), 1)
	if err := os.WriteFile(source.config, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	source.start(t)
	source.read(t, 1)
	source.waitACKs(t, 0, 1)
	source.waitRetry(t, "hub")
	if len(hub.entries(t)) != 0 || len(central.snapshot(t, "snapshot").Rows) != 0 {
		t.Fatal("expired TLS client certificate accepted raw input")
	}
	source.stop(t)
	config = strings.Replace(config, "expired-client.pem", "client.pem", 1)
	if err := os.WriteFile(source.config, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	source.start(t)
	// Recovery sends the immutable pending packet without another read; the
	// independently acknowledged Desk destination must not be re-enrolled.
	source.waitACKs(t, 1, 1)
	central.waitRawRows(t, 1)
	before := central.snapshot(t, "snapshot")
	if before.Results != 0 || before.MemberResults != 0 || before.Finished != 0 {
		t.Fatal("raw-only TLS acceptance changed outcomes")
	}
	source.read(t, 2)
	source.waitACKs(t, 2, 2)
	central.waitRawRows(t, 2)
	beforeRelay := central.snapshot(t, "snapshot")
	relay := NewEdgeRelayManager(events, log.New(io.Discard, "", 0), 10*time.Millisecond)
	t.Cleanup(relay.StopAll)
	if _, err := relay.Configure(t.Context(), "100", domain.EdgeRelayConfig{Endpoint: "tls://" + hub.endpoint, Enabled: true, TLSBundle: security.relayDir}, true); err != nil {
		t.Fatal(err)
	}
	waitRelayProgress(t, store, func(progress sqlite.EdgeRelayProgress) bool { return progress.Acked == 2 && progress.Pending == 0 })
	central.waitRawRows(t, 2)
	if after := central.snapshot(t, "snapshot"); !reflect.DeepEqual(beforeRelay.Rows, after.Rows) {
		t.Fatal("independently credentialed Desk relay overwrote raw metadata")
	}
	accepted := len(hub.entries(t))
	instant := time.Now().UTC().Truncate(time.Millisecond)
	wrong := ingest.Event{EdgeVersion: 1, ExternalEventID: 100, SourceSessionID: "100", IdentityProfile: edge.IdentityRFID, ClockEvidenceID: "synthetic-clock", ClockQuality: edge.ClockSource,
		Board: "Feibot:other-synthetic", EPC: "E2009999", Time: instant.UnixMilli(), RTC: instant.Format(time.RFC3339Nano), Ant: 1,
		ObservationVersion: 1, CaptureSourceID: "other-synthetic", OriginSystem: "edge", OriginInstanceID: "synthetic-other", OriginSequence: 1}
	wrong.ID = ingest.RFIDReadID(wrong.Board, wrong.EPC, wrong.Time, wrong.Ant)
	payload, err := edge.Encode(wrong)
	if err != nil {
		t.Fatal(err)
	}
	client := tcp.LineClient{Endpoint: "tls://" + hub.endpoint, Timeout: 500 * time.Millisecond, TLSConfig: security.clientTLS}
	defer client.Close()
	if err := client.Send(t.Context(), payload, strings.TrimSpace(string(edge.ACK(wrong)))); err == nil {
		t.Fatal("signed key impersonated another admitted board")
	}
	if len(hub.entries(t)) != accepted {
		t.Fatal("unauthorized certificate/board reached Redis")
	}
	plain := tcp.LineClient{Endpoint: hub.endpoint, Timeout: 500 * time.Millisecond}
	defer plain.Close()
	if err := plain.Send(t.Context(), payload, strings.TrimSpace(string(edge.ACK(wrong)))); err == nil {
		t.Fatal("public TLS listener downgraded to plaintext")
	}
	final := central.snapshot(t, "snapshot")
	if len(final.Rows) != 2 || final.Results != 0 || final.MemberResults != 0 || final.Finished != 0 {
		t.Fatal("TLS rejection changed raw facts or outcomes")
	}
	for _, row := range final.Rows {
		if row["board"] != "Feibot:U659" || row["source_session_id"] != "100" || row["origin_system"] != "edge" {
			t.Fatal("TLS transport changed source identity")
		}
	}
	var localRaw, nativeOutbox int
	if err := store.DB().QueryRow(`SELECT (SELECT COUNT(*) FROM rfid_logs),(SELECT COUNT(*) FROM observation_outbox)`).Scan(&localRaw, &nativeOutbox); err != nil || localRaw != 2 || nativeOutbox != 0 {
		t.Fatal("TLS Hub retry changed independent Desk ownership")
	}
	t.Log("actual non-root CSR + offline issuance + Edge/Hub mTLS + independently credentialed Desk relay + Redis/sync/MySQL raw passed; expired client certificate retained Hub queue while Desk ACKed; renewal recovered without new RFID; relay dedup preserved raw; wrong board/plaintext rejected; no checkpoints/results")
}
