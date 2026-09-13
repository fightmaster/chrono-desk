//go:build linux && edgeintegration && edgecentralintegration

package service

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real binaries, vendor files, mTLS, HTTP controller, Redis consumer and MySQL.
// No source grants, board assignment or checkpoints are seeded by this test.
func TestAutomaticFeibotVendorDeskAndCentralRawWithoutBindings(t *testing.T) {
	security := newEdgeChainTLS(t)
	security.automaticFeibot = true
	hub := newEdgeChainHubTopology(t, 100, "Feibot:U659", "100", "none", false, security)
	central := newEdgeChainCentral(t, hub, "Feibot:U659", "100", "setup-auto")
	store, _ := edgeLiveFixture(t)
	if _, err := store.DB().Exec(`DELETE FROM checkpoints; DELETE FROM edge_bindings; DELETE FROM edge_input_policy; DELETE FROM local_changes WHERE entity='edge_binding'`); err != nil {
		t.Fatal(err)
	}
	manager := NewLiveManager(log.New(io.Discard, "", 0))
	t.Cleanup(manager.StopAll)
	port := edgeTestPort(t)
	if err := manager.Start(store, "100", port); err != nil {
		t.Fatal(err)
	}
	source := newEdgeChainSource(t, "feibot", hub.endpoint, "127.0.0.1:"+port)
	vendor := filepath.Join(source.dir, "vendor")
	source.csvPath = filepath.Join(vendor, "data", "U659_100")
	if err := os.MkdirAll(source.csvPath, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(vendor, "machine.ini"), "[machine]\nid=U659\n")
	write(filepath.Join(vendor, "run.ini"), "[machine]\neventConfigFileName=ec_999.ecg\n")
	write(filepath.Join(vendor, "ec_999.ecg"), "[event]\nid=999\ntimezone=UTC\n")
	write(filepath.Join(vendor, "Newevent.ini"), "100\n")
	writeVendor := func(port string) {
		write(filepath.Join(vendor, "user.ini"), "[socket]\ntcp_targetIp=127.0.0.1\ntcp_targetPort="+port+"\n")
	}
	writeVendor(port)
	data, err := os.ReadFile(source.config)
	if err != nil {
		t.Fatal(err)
	}
	config := string(data)
	for _, pair := range [][2]string{
		{"root =\n", "root = " + vendor + "\n"},
		{"machine_ini_path =\n", "machine_ini_path = " + filepath.Join(vendor, "machine.ini") + "\n"},
		{"run_state_path =\n", "run_state_path = " + filepath.Join(vendor, "run.ini") + "\n"},
		{"event_config_dir =\n", "event_config_dir = " + vendor + "\n"},
		{"user_config_path =\n", "user_config_path = " + filepath.Join(vendor, "user.ini") + "\n"},
		{"endpoint = tcp://" + hub.endpoint, "endpoint = tls://" + hub.endpoint},
		{"[destination.hub]\n", fmt.Sprintf("[destination.hub]\ntls_certificate_file = %s\ntls_key_file = %s\ntls_server_ca_file = %s\ntls_server_name = localhost\n", filepath.Join(security.clientDir, "client.pem"), filepath.Join(security.clientDir, "client-key.pem"), filepath.Join(security.clientDir, "ca.pem"))},
	} {
		config = strings.Replace(config, pair[0], pair[1], 1)
	}
	write(source.config, config)
	source.start(t)
	for n := 1; n <= 5; n++ {
		write(filepath.Join(source.csvPath, fmt.Sprintf("U659_%d_00100.csv", n)), fmt.Sprintf("E200%04d:%s,port=%d,rssi=28\n", n, time.Now().UTC().Format("2006-01-02_15:04:05.000"), n))
	}
	source.waitACKs(t, 0, 5)
	source.waitRetry(t, "hub")
	before := central.snapshot(t, "snapshot")
	if len(before.Rows) != 0 || len(before.SourceBindings) != 0 || len(before.BoardEvents) != 0 {
		t.Fatal("offline admission invented RAW or permissions")
	}
	owners, _ := json.Marshal(map[string]any{security.publicKey: map[string]any{"board": "Feibot:U659", "actor_id": 1, "site_code": "chrono"}})
	// The synthetic same-host HTTP fixture explicitly configures its default
	// surface, exercising normal host-context resolution, not a bypass route.
	central.startHTTP(t, "RFID_AUTONOMOUS_FEIBOT_CLIENTS="+string(owners), "PWA_INTERNAL_HUB_KEY=synthetic-hub-only-key", "PLATFORM_DEFAULT_SITE_CODE=chrono", "PLATFORM_DEFAULT_SURFACE=app")
	source.waitACKs(t, 5, 5)
	central.waitRawRows(t, 5)
	accepted := central.snapshot(t, "snapshot")
	if len(accepted.SourceBindings) != 1 || len(accepted.Actions) != 1 || accepted.Actions[0] != "grant" || len(accepted.BoardEvents) != 0 {
		t.Fatal("automatic RAW permission created board/checkpoint settings or duplicate audit")
	}
	ports := map[string]bool{}
	for _, row := range accepted.Rows {
		ports[fmt.Sprint(row["ant"])] = true
		if row["board"] != "Feibot:U659" || row["source_session_id"] != "100" || row["origin_system"] != "edge" {
			t.Fatal("vendor event/source metadata lost")
		}
	}
	if len(ports) != 5 {
		t.Fatalf("antenna/port filtered: %v", ports)
	}
	source.stop(t)
	manager.StopEdge("100")
	newPort := edgeTestPort(t)
	if err := manager.Start(store, "100", newPort); err != nil {
		t.Fatal(err)
	}
	writeVendor(newPort)
	source.start(t)
	source.read(t, 6)
	source.waitACKs(t, 6, 6)
	central.waitRawRows(t, 6)
	final := central.snapshot(t, "snapshot")
	if len(final.SourceBindings) != 1 || len(final.Actions) != 1 || len(final.BoardEvents) != 0 {
		t.Fatal("restart rewrote automatic permission")
	}
	var raw, bindings, native, checkpoints int
	if err := store.DB().QueryRow(`SELECT (SELECT COUNT(*) FROM rfid_logs),(SELECT COUNT(*) FROM edge_bindings),(SELECT COUNT(*) FROM observation_outbox),(SELECT COUNT(*) FROM checkpoints)`).Scan(&raw, &bindings, &native, &checkpoints); err != nil || raw != 6 || bindings != 0 || native != 0 || checkpoints != 0 {
		t.Fatalf("Desk raw=%d bindings=%d native=%d checkpoints=%d error=%v", raw, bindings, native, checkpoints, err)
	}
	t.Log("automatic Feibot: vendor event overrides stale ecg; ordinary Desk input without binding; mTLS owner-scoped HTTP admission without board/checkpoint; five independent ports preserved; offline site retained Hub work while Desk ACKed; site recovery and vendor-port restart drained immutable RAW with one audit and no results")
}
