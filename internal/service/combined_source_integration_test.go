//go:build linux && edgeintegration

package service

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

// Actual Edge executable, synthetic read-only vendor inputs, real Desk TCP and
// SQLite. The second destination is deliberately offline: no synthetic ACK,
// production endpoint or live appliance participates in this test.
func TestCombinedFeibotSourceFollowsVendorPortAcrossRestart(t *testing.T) {
	store, observation := edgeLiveFixture(t)
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
	offline, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = offline.Close() })
	source := newEdgeChainSource(t, "feibot", offline.Addr().String(), "127.0.0.1:"+port)
	vendor := filepath.Join(source.dir, "vendor")
	source.csvPath = filepath.Join(vendor, "data", "U659_100")
	if err := os.MkdirAll(source.csvPath, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(vendor, "machine.ini"), "[machine]\nid=U659\n")
	write(filepath.Join(vendor, "run.ini"), "[machine]\neventConfigFileName=ec_100.ecg\n")
	write(filepath.Join(vendor, "ec_100.ecg"), "[event]\nid=100\ntimezone=UTC\n")
	userConfig := filepath.Join(vendor, "user.ini")
	writeVendor := func(port string) {
		write(userConfig, fmt.Sprintf("[socket]\ntcp_targetIp=127.0.0.1\ntcp_targetPort=%s\n", port))
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
		{"user_config_path =\n", "user_config_path = " + userConfig + "\n"},
		{"[destination.chrono]\n", "[destination.chrono]\nfollow_vendor_endpoint = true\n"},
	} {
		config = strings.Replace(config, pair[0], pair[1], 1)
	}
	write(source.config, config)
	source.start(t)
	source.read(t, 1)
	source.waitACKs(t, 0, 1)
	source.waitRetry(t, "hub")
	source.stop(t)
	manager.StopEdge("100")
	newPort := edgeTestPort(t)
	if err := manager.StartCombined(store, "100", newPort); err != nil {
		t.Fatal(err)
	}
	// Only the synthetic vendor setting changes. No sidecar UI/config/session
	// change accompanies the normal process restart and next observation.
	writeVendor(newPort)
	source.start(t)
	source.read(t, 2)
	source.waitACKs(t, 0, 2)
	var raw, edgeOutbox, nativeOutbox, results int
	if err := store.DB().QueryRow(`SELECT (SELECT COUNT(*) FROM rfid_logs),(SELECT COUNT(*) FROM edge_observation_outbox),(SELECT COUNT(*) FROM observation_outbox),(SELECT COUNT(*) FROM results)`).Scan(&raw, &edgeOutbox, &nativeOutbox, &results); err != nil {
		t.Fatal(err)
	}
	if raw != 2 || edgeOutbox != 2 || nativeOutbox != 0 || results != 0 {
		t.Fatalf("raw=%d edge=%d native=%d results=%d", raw, edgeOutbox, nativeOutbox, results)
	}
	journal, err := store.EdgeJournal(t.Context(), "100", 0, 10)
	if err != nil || len(journal) != 2 {
		t.Fatalf("source journal: %v %v", journal, err)
	}
	t.Log("actual Edge vendor port follow/restart passed; Desk ACK/raw durable; independent offline Hub work remains pending")
}
