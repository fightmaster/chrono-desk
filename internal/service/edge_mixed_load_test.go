//go:build linux && edgeintegration && edgecentralintegration && edgeload

package service

import (
	"bufio"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/fightmaster1/rfid-core/tcp"
)

// Compare the actual sidecar and Hub/Redis with/without the client heartbeat.
// The local HTTPS responder has no database: backend acceptance is separate.
// The second observation peer is core-backed, not a packaged Desk application.
func TestEdgeMixedNativeLoadAndSidecarBacklog(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(fmt.Sprintf("heartbeat_%t", managed), func(t *testing.T) {
			hub := newEdgeChainHubTopology(t, 100, "Feibot:U659", "100", "none", true)
			var online atomic.Bool
			edgeAddress := hub.tunnelPort(t, "44004", &online)
			feibotAddress := hub.tunnelPort(t, "44003", nil)
			myraceAddress := hub.tunnelPort(t, "44002", nil)
			peerAddress, peer := newEdgeLoadPeer(t)
			source := newEdgeChainSource(t, "feibot", edgeAddress, peerAddress)
			config, err := os.ReadFile(source.config)
			if err != nil || strings.Count(string(config), "batch_size = 100\n") != 1 {
				t.Fatal("missing standard source batch setting", err)
			}
			if err := os.WriteFile(source.config, []byte(strings.Replace(string(config), "batch_size = 100\n", "batch_size = 500\n", 1)), 0600); err != nil {
				t.Fatal(err)
			}
			var heartbeats atomic.Int64
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					RequestID string `json:"request_id"`
					Profile   string `json:"profile"`
				}
				if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("d", 64) || json.NewDecoder(http.MaxBytesReader(w, r.Body, 32768)).Decode(&request) != nil || request.Profile != "feibot" {
					http.Error(w, "invalid synthetic heartbeat", 400)
					return
				}
				heartbeats.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"version": 1, "request_id": request.RequestID, "command": nil, "acknowledged_results": []string{}})
			}))
			t.Cleanup(server.Close)
			ca := filepath.Join(t.TempDir(), "ca.pem")
			if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
				t.Fatal(err)
			}
			source.extraEnv = []string{"SSL_CERT_FILE=" + ca, "SSL_CERT_DIR=" + filepath.Dir(ca)}
			source.start(t)
			if managed {
				enrollManagement(t, source, server.URL, "00000000-0000-4000-8000-000000000001", strings.Repeat("d", 64))
			}
			const backlog = 15000
			writer := newEdgeLoadInput(t, source)
			for first := 1; first <= backlog; first += 5000 {
				writer.write(t, first, 5000)
			}
			writer.close(t)
			var resources edgeLoadResources
			waitEdgeLoad(t, source, &resources, 45*time.Second, "preloaded backlog and independent second-peer ACKs", func() bool {
				st, err := source.status()
				if err != nil {
					return false
				}
				for _, d := range st.Destinations {
					if d.ID == "chrono" && d.Acknowledged == backlog {
						return true
					}
				}
				return false
			})
			source.waitRetry(t, "hub")
			before := edgeSwitchRows(t, source)
			if len(before) != backlog {
				t.Fatal("backlog was not durably captured")
			}
			peer.mu.Lock()
			peerCalls := peer.calls
			peer.mu.Unlock()
			results := make(chan mixedNativeResult, 2)
			started := time.Now()
			go runMixedNative(feibotAddress, "feibot", started, results)
			go runMixedNative(myraceAddress, "myrace", started, results)
			for tick := 1; tick <= 60; tick++ {
				time.Sleep(max(0, time.Until(started.Add(time.Duration(tick)*time.Second))))
				if tick == 10 {
					online.Store(true)
				}
				resources.sample(t, source)
			}
			for range 2 {
				result := <-results
				t.Logf("native %s: count=%d elapsed=%s max batch ACK=%s max schedule delay=%s", result.profile, result.count, result.elapsed, result.maxACK, result.maxLate)
				if result.err != nil || result.elapsed > 65*time.Second || result.maxLate > 2*time.Second {
					t.Fatalf("native stream failed its scheduled rate: %v", result.err)
				}
			}
			st, err := source.status()
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range st.Destinations {
				t.Logf("delivery at native-stream deadline: destination=%s acknowledged=%d pending=%d retry=%d", d.ID, d.Acknowledged, d.Pending, d.Retry)
				if d.Acknowledged != backlog || d.Pending+d.Retry != 0 {
					// Keep the gate red but collect resources and immutable-state evidence.
					t.Error("sidecar backlog did not drain during the live native streams")
				}
			}
			if (managed && heartbeats.Load() < 2) || (!managed && heartbeats.Load() != 0) {
				t.Fatal("heartbeat mode was not exercised")
			}
			entries := hub.entries(t)
			counts, ids := map[string]int{}, map[string]bool{}
			for _, entry := range entries {
				if entry["id"] == "" || ids[entry["id"]] {
					t.Fatal("missing or duplicate Redis observation identity")
				}
				ids[entry["id"]] = true
				counts[entry["board"]]++
			}
			if counts["Feibot:U659"] != backlog || counts["Feibot:U660"] != 15000 || counts["MyRaceNano:446365"] != 60 || len(entries) != backlog+15060 {
				t.Errorf("mixed persisted stream counts: %v", counts)
			}
			peer.mu.Lock()
			unchanged := peer.calls == peerCalls && len(peer.packets) == backlog
			peer.mu.Unlock()
			if !unchanged {
				t.Fatal("healthy destination was replayed during Hub recovery")
			}
			after := edgeSwitchRows(t, source)
			if len(after) != len(before) {
				t.Fatal("backlog cardinality changed during recovery")
			}
			for id, row := range before {
				if after[id] != row {
					t.Fatal("backlog content changed during recovery")
				}
			}
			resources.sample(t, source)
			hubResources := hub.docker(t, "exec", hub.id, "/bin/sh", "-c", "cat /proc/1/status /sys/fs/cgroup/memory.peak /sys/fs/cgroup/cpu.stat")
			for _, line := range strings.Split(hubResources, "\n") {
				if strings.HasPrefix(line, "VmHWM:") || strings.HasPrefix(line, "usage_usec ") || (len(strings.Fields(line)) == 1 && len(line) > 0 && line[0] >= '0' && line[0] <= '9') {
					t.Log("Hub/Redis fixture resource:", line)
				}
			}
			source.stop(t)
			cpu := source.process.ProcessState.UserTime() + source.process.ProcessState.SystemTime()
			t.Logf("sidecar: heartbeat=%t exchanges=%d CPU=%s peakRSS=%.2fMiB FD=%d threads=%d DB+WAL+SHM=%.2fMiB logs=%.2fKiB", managed, heartbeats.Load(), cpu, float64(resources.rssKiB)/1024, resources.fds, resources.threads, float64(resources.dbBytes)/(1<<20), float64(resources.logBytes)/1024)
			if resources.rssKiB > 96*1024 || resources.fds > 64 {
				t.Fatal("sidecar exceeded the local smoke resource budget")
			}
		})
	}
}

type mixedNativeResult struct {
	profile                  string
	count                    int
	elapsed, maxACK, maxLate time.Duration
	err                      error
}

func runMixedNative(address, profile string, started time.Time, results chan<- mixedNativeResult) {
	result := mixedNativeResult{profile: profile}
	defer func() { result.elapsed = time.Since(started); results <- result }()
	conn, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		result.err = err
		return
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	batches, batch, period := 600, 25, 100*time.Millisecond
	if profile == "myrace" {
		batches, batch, period = 60, 1, time.Second
	}
	for i := 0; i < batches; i++ {
		due := started.Add(time.Duration(i) * period)
		time.Sleep(max(0, time.Until(due)))
		result.maxLate = max(result.maxLate, time.Since(due))
		var wire strings.Builder
		ack := "ok\n"
		for j := 0; j < batch; j++ {
			n := i*batch + j + 1
			if profile == "feibot" {
				fmt.Fprintf(&wire, "{\"DeviceCode\":\"U660\",\"epc\":\"E300%08X\",\"time\":%q,\"channelId\":1}\n", n, started.Add(time.Duration(n)*time.Millisecond).UTC().Format(time.RFC3339Nano))
			} else {
				payload := strconv.Itoa(n) + ";20260911;120000;100;1;446365;0"
				event, parseErr := (tcp.MyRaceNanoAdapter{Location: time.UTC}).Parse([]byte(payload))
				if parseErr != nil {
					result.err = parseErr
					return
				}
				ack = event.ID + "\n"
				wire.WriteString(payload + "\n")
			}
		}
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		sent := time.Now()
		if _, err := io.WriteString(conn, wire.String()); err != nil {
			result.err = err
			return
		}
		for j := 0; j < batch; j++ {
			got, err := reader.ReadString('\n')
			if err != nil || got != ack {
				result.err = fmt.Errorf("%s acknowledgement failed: %v", profile, err)
				return
			}
			result.count++
		}
		result.maxACK = max(result.maxACK, time.Since(sent))
	}
}
