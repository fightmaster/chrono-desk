//go:build linux && edgeintegration

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/rfid-core/edge"
)

// This opt-in suite launches the actual sidecar executable and actual Hub main
// with Redis in an isolated Docker network. Desk uses its production service,
// listener, SQLite projection and relay. There is no fake receiver or fake ACK.
// It intentionally does not claim central MySQL/RUN5 or appliance acceptance.
func TestEdgeChainSidecarReceiversAndRelay(t *testing.T) {
	for _, profile := range []string{"feibot", "plate"} {
		t.Run(profile, func(t *testing.T) {
			runEdgeChainReceivers(t, profile, nil)
		})
	}
}

// The optional central gate reuses these exact source/outage/relay checks and
// then consumes the real Redis backlog with rfid-sync and RUN5's MySQL schema.
func runEdgeChainReceivers(t *testing.T, profile string, central func(*testing.T, *edgeChainHub, *edgeChainSource, *sqlite.Store)) {
	ctx := context.Background()
	board, session := "Feibot:U659", "100"
	if profile == "plate" {
		board, session = "plate-test", "plate-one"
	}
	hub := newEdgeChainHub(t, board, session)
	logger := log.New(io.Discard, "", 0)
	catalog, err := sqlite.NewEventCatalog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	events := NewEventService(catalog, logger)
	t.Cleanup(events.Close)
	store, err := catalog.OpenOrCreate("100")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-time.Minute).UnixMilli()
	for _, err := range []error{
		store.UpsertEvent(ctx, domain.Event{ID: "100", Name: "Synthetic receiver chain", Timezone: "UTC"}),
		store.UpsertRace(ctx, domain.Race{ID: "race", EventID: "100", Name: "Race", StartedAtMs: &start}),
		store.UpsertCheckpoint(ctx, domain.Checkpoint{ID: "finish", EventID: "100", RaceID: "race", Board: board, Type: domain.CheckpointType(3), Sort: 1}),
		store.SetEdgeBindings(ctx, "100", []domain.EdgeBinding{{Board: board, SourceSessionID: session}}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	for n := 1; n <= 3; n++ {
		// Plate retains the source-backed last-six-hex-digits bib rule;
		// Feibot identifies the same fixture through its full EPC.
		epc := fmt.Sprintf("E200%04d", n)
		number := int64(n)
		if err := store.UpsertMember(ctx, domain.Member{ID: strconv.Itoa(n), EventID: "100", RaceID: "race", Number: &number, EPC: &epc}); err != nil {
			t.Fatal(err)
		}
	}
	live := NewLiveManager(logger)
	t.Cleanup(live.StopAll)
	port := edgeTestPort(t)
	if err := live.StartEdge(store, "100", port); err != nil {
		t.Fatal(err)
	}
	source := newEdgeChainSource(t, profile, hub.endpoint, "127.0.0.1:"+port)
	source.start(t)
	if profile == "plate" {
		source.confirmClock(t)
	}
	source.read(t, 1)
	source.waitACKs(t, 1, 1)
	assertEdgeChainRows(t, store, 1)
	t.Log("initial read committed and ACKed by both real receivers")

	// A stopped Desk must not hold back Hub. After a same-boot process
	// restart, no new read, Start button or clock confirmation is needed
	// to send the existing packet only to the missing destination.
	live.StopEdge("100")
	source.read(t, 2)
	source.waitACKs(t, 2, 1)
	assertEdgeChainRows(t, store, 1)
	source.stop(t)
	beforeRestart := len(hub.entries(t))
	if err := live.StartEdge(store, "100", port); err != nil {
		t.Fatal(err)
	}
	source.start(t)
	source.waitACKs(t, 2, 2)
	assertEdgeChainRows(t, store, 2)
	if entries := hub.entries(t); len(entries) != beforeRestart {
		t.Fatalf("already ACKed Hub packets were replayed: before=%d after=%d", beforeRestart, len(entries))
	}
	t.Log("Desk outage and sidecar restart recovered without new input; Hub ACKs retained")

	// Freeze only the owned Hub/Redis container. Desk keeps accepting
	// while Hub delivery becomes retryable. Unfreezing must drain it.
	if profile == "plate" {
		source.confirmClock(t) // new process needs new evidence for NEW raw reads
	}
	hub.docker(t, "pause", hub.id)
	source.read(t, 3)
	source.waitACKs(t, 2, 3)
	source.waitRetry(t, "hub")
	assertEdgeChainRows(t, store, 3)
	hub.docker(t, "unpause", hub.id)
	source.waitACKs(t, 3, 3)
	t.Log("Hub outage did not block Desk; original pending packet drained automatically")

	// The actual Desk relay publishes its captured source journal back
	// through Hub. It must not convert the packets to Desk-owned v3.
	relay := NewEdgeRelayManager(events, logger, 10*time.Millisecond)
	t.Cleanup(relay.StopAll)
	if _, err := relay.Configure(ctx, "100", domain.EdgeRelayConfig{Endpoint: hub.endpoint, Enabled: true}, true); err != nil {
		t.Fatal(err)
	}
	edgeChainWait(t, "Desk source relay ACKs", func() bool {
		p, err := store.EdgeRelayProgress(ctx, "100")
		return err == nil && p.Acked == 3 && p.Pending == 0
	})
	relay.StopAll()
	assertEdgeChainRows(t, store, 3)
	journal, err := store.EdgeJournal(ctx, "100", 0, 10)
	if err != nil || len(journal) != 3 {
		t.Fatalf("source journal: %d %v", len(journal), err)
	}
	packets := map[string]map[string]string{}
	for _, item := range journal {
		packet, err := edge.Decode(item.Payload)
		if err != nil || packet.Board != board || packet.SourceSessionID != session || packet.ExternalEventID != 100 {
			t.Fatalf("Desk changed source binding: %+v %v", packet, err)
		}
		packets[packet.ID] = map[string]string{
			"board": board, "external_event_id": "100", "source_session_id": session,
			"origin_instance_id": packet.OriginInstanceID, "origin_system": packet.OriginSystem,
			"origin_sequence": strconv.FormatUint(packet.OriginSequence, 10), "edge_version": "1",
			"epc": packet.EPC, "time": strconv.FormatInt(packet.Time, 10),
			"identity_profile": packet.IdentityProfile, "clock_quality": packet.ClockQuality,
			"clock_evidence_id": packet.ClockEvidenceID,
			"rtc":               packet.RTC, "ant": fmt.Sprint(packet.Ant), "status": fmt.Sprint(packet.Status),
			"rssi": fmt.Sprint(packet.RSSI), "pc": packet.PC, "cnt": fmt.Sprint(packet.Cnt), "number": fmt.Sprint(packet.Number),
			"capture_source_id": packet.CaptureSourceID, "observation_version": "1",
		}
	}
	seen := map[string]int{}
	for _, entry := range hub.entries(t) {
		want, ok := packets[entry["id"]]
		if !ok {
			t.Fatalf("unexpected Hub packet: %v", entry)
		}
		for key, value := range want {
			if entry[key] != value {
				t.Fatalf("Hub/Desk %s changed: got %q want %q", key, entry[key], value)
			}
		}
		seen[entry["id"]]++
	}
	for id := range packets {
		if seen[id] < 2 {
			t.Fatalf("missing direct/Desk-relay paths for %s: %d", id, seen[id])
		}
	}
	t.Log("Desk relay preserved event/session/clock/origin on all three real Hub publications")
	if central != nil {
		central(t, hub, source, store)
	}
}

func assertEdgeChainRows(t *testing.T, store *sqlite.Store, want int) {
	t.Helper()
	var raw, edgeRows, native, results int
	err := store.DB().QueryRow(`SELECT (SELECT COUNT(*) FROM rfid_logs), (SELECT COUNT(*) FROM edge_observation_outbox), (SELECT COUNT(*) FROM observation_outbox), (SELECT COUNT(*) FROM results)`).Scan(&raw, &edgeRows, &native, &results)
	if err != nil || raw != want || edgeRows != want || native != 0 || results != want {
		t.Fatalf("Desk state raw=%d edge=%d native=%d results=%d want=%d err=%v", raw, edgeRows, native, results, want, err)
	}
}

func edgeChainWait(t *testing.T, label string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out: %s", label)
}

type edgeChainSource struct {
	profile, binary, dir, admin, reader, config string
	stopProcess                                 func(*testing.T)
}

func newEdgeChainSource(t *testing.T, profile, hub, desk string) *edgeChainSource {
	t.Helper()
	s := &edgeChainSource{profile: profile, binary: edgeChainBinary(t, "EDGE_SIDECAR_BINARY"), dir: t.TempDir(), admin: "127.0.0.1:" + edgeTestPort(t), reader: "127.0.0.1:" + edgeTestPort(t)}
	s.config = filepath.Join(s.dir, "config")
	db := filepath.Join(s.dir, "sidecar.db")
	var data []byte
	if profile == "plate" {
		data, _ = json.Marshal(map[string]any{
			"schema_version": 1, "database_path": db, "admin_listen": s.admin, "admin_token": "synthetic-chain-key", "reader_listen": s.reader, "max_connections": 2,
			"initial": map[string]any{
				"source":         map[string]any{"session_id": "plate-one", "event_id": 100, "device_code": "plate-test", "board": "plate-test", "instance_id": "plate-installation", "timezone": "UTC"},
				"reader_address": "", "clock": map[string]int{"past_seconds": 3600, "future_seconds": 60, "max_age_seconds": 3600, "max_step_ms": 1000},
				"destinations": []map[string]any{{"id": "hub", "endpoint": "tcp://" + hub, "enabled": true, "timeout_ms": 2000}, {"id": "chrono", "endpoint": "tcp://" + desk, "enabled": true, "timeout_ms": 2000}},
			},
		})
	} else {
		data = []byte(fmt.Sprintf(`[sidecar]
autonomous = true
database_path = %s
[admin]
listen = %s
token = synthetic-chain-key
[feibot]
root =
machine_ini_path =
run_state_path =
event_config_dir =
user_config_path =
csv_path = %s
[event]
feibot_event_id = 100
destination_event_id = 100
device_code = U659
timezone = UTC
[ingest]
scan_interval = 100ms
batch_size = 100
[destination.hub]
kind = rfid-hub
protocol = edge_observation_v1
endpoint = tcp://%s
enabled = true
timeout = 2s
[destination.chrono]
kind = chrono-desk
protocol = edge_observation_v1
endpoint = tcp://%s
enabled = true
timeout = 2s
`, db, s.admin, filepath.Join(s.dir, "csv"), hub, desk))
		if err := os.Mkdir(filepath.Join(s.dir, "csv"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(s.config, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return s
}

func (s *edgeChainSource) start(t *testing.T) {
	t.Helper()
	cmd := exec.Command(s.binary, "run", "-profile", s.profile, "-config", s.config)
	cmd.Dir = s.dir
	// Do not inherit credentials, proxies or the host's system D-Bus address.
	cmd.Env = []string{"PATH=/usr/bin:/bin", "TZ=UTC", "DBUS_SYSTEM_BUS_ADDRESS=unix:path=/nonexistent/edge-chain-test"}
	output, err := os.CreateTemp(s.dir, "process-*.log")
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Start(); err != nil {
		_ = output.Close()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var once sync.Once
	s.stopProcess = func(t *testing.T) {
		t.Helper()
		once.Do(func() {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("sidecar exit: %v", err)
				}
			case <-time.After(8 * time.Second):
				_ = cmd.Process.Kill()
				<-done
				t.Error("sidecar did not stop gracefully")
			}
			_ = output.Close()
			if t.Failed() {
				data, _ := os.ReadFile(output.Name())
				t.Logf("sidecar log: %s", data)
			}
		})
	}
	stop := s.stopProcess
	t.Cleanup(func() { stop(t) })
	edgeChainWait(t, "sidecar HTTP startup", func() bool { _, err := s.status(); return err == nil })
}

func (s *edgeChainSource) stop(t *testing.T) { s.stopProcess(t) }

func edgeChainHTTP() *http.Client {
	return &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (s *edgeChainSource) request(method, path string, form url.Values) ([]byte, int, error) {
	req, err := http.NewRequest(method, "http://"+s.admin+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("X-Feibot-Token", "synthetic-chain-key")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := edgeChainHTTP().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	return data, resp.StatusCode, err
}

type edgeChainSourceStatus struct {
	Revision     int64 `json:"revision"`
	Destinations []struct {
		ID           string `json:"id"`
		Acknowledged int    `json:"acknowledged"`
		Pending      int    `json:"pending"`
		Retry        int    `json:"retry"`
	} `json:"destinations"`
}

func (s *edgeChainSource) status() (edgeChainSourceStatus, error) {
	var st edgeChainSourceStatus
	data, code, err := s.request(http.MethodGet, "/api/status", nil)
	if err != nil {
		return st, err
	}
	if code != 200 {
		return st, fmt.Errorf("status %d: %s", code, data)
	}
	err = json.Unmarshal(data, &st)
	return st, err
}

func (s *edgeChainSource) waitACKs(t *testing.T, hub, desk int) {
	t.Helper()
	var last edgeChainSourceStatus
	edgeChainWait(t, fmt.Sprintf("sidecar ACKs hub=%d desk=%d", hub, desk), func() bool {
		var err error
		last, err = s.status()
		if err != nil || len(last.Destinations) != 2 {
			return false
		}
		for _, d := range last.Destinations {
			want := hub
			if d.ID == "chrono" {
				want = desk
			}
			if d.Acknowledged != want {
				return false
			}
		}
		return true
	})
	if hub == desk {
		for _, d := range last.Destinations {
			if d.Pending+d.Retry != 0 {
				t.Fatalf("ACKed queue still pending: %+v", last)
			}
		}
	}
}

func (s *edgeChainSource) waitRetry(t *testing.T, id string) {
	t.Helper()
	edgeChainWait(t, "persisted retry for "+id, func() bool {
		st, err := s.status()
		if err != nil {
			return false
		}
		for _, d := range st.Destinations {
			if d.ID == id {
				return d.Retry > 0
			}
		}
		return false
	})
}

func (s *edgeChainSource) confirmClock(t *testing.T) {
	t.Helper()
	for _, action := range []string{"pause", "confirm_clock", "resume"} {
		st, err := s.status()
		if err != nil {
			t.Fatal(err)
		}
		page, code, err := s.request(http.MethodGet, "/", nil)
		if err != nil || code != 200 {
			t.Fatalf("plate form: %d %v", code, err)
		}
		match := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindSubmatch(page)
		if len(match) != 2 {
			t.Fatal("plate CSRF field missing")
		}
		form := url.Values{"csrf_token": {string(match[1])}, "revision": {strconv.FormatInt(st.Revision, 10)}, "action": {action}, "time": {time.Now().UTC().Format(time.RFC3339Nano)}}
		body, code, err := s.request(http.MethodPost, "/actions", form)
		if err != nil || code != http.StatusSeeOther {
			t.Fatalf("plate %s: %d %s %v", action, code, body, err)
		}
	}
}

func (s *edgeChainSource) read(t *testing.T, n int) {
	t.Helper()
	now := time.Now().UTC()
	if s.profile == "plate" {
		conn, err := net.DialTimeout("tcp", s.reader, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
		if _, err := fmt.Fprintf(conn, "{\"RTC\":%q,\"EPC\":\"E200%04d\",\"ANT\":1}\n", now.Format("15:04:05.000"), n); err != nil {
			t.Fatal(err)
		}
		return
	}
	// Each new file exercises Feibot's multi-file event capture, not a direct
	// injection of a normalized observation into the sidecar database.
	data := fmt.Sprintf("E200%04d:%s,port=1,rssi=28\n", n, now.Format("2006-01-02_15:04:05.000"))
	if err := os.WriteFile(filepath.Join(s.dir, "csv", fmt.Sprintf("U659_%d_00100.csv", n)), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func edgeChainBinary(t *testing.T, key string) string {
	t.Helper()
	path := os.Getenv(key)
	info, err := os.Stat(path)
	if !filepath.IsAbs(path) || err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("%s must name an existing executable; see docs/edge-chain-integration.md", key)
	}
	return path
}
