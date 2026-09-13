//go:build linux && edgeintegration && edgecentralintegration

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/rfid-core/edge"
)

// This is a separate explicit gate: the receiver-only suite remains runnable
// without PHP/MySQL/rfid-sync. Missing central prerequisites are failures.
func TestEdgeChainCentralAdmission(t *testing.T) {
	for _, profile := range []string{"feibot", "plate"} {
		t.Run(profile, func(t *testing.T) { runEdgeChainReceivers(t, profile, assertEdgeChainCentral) })
	}
}

func assertEdgeChainCentral(t *testing.T, hub *edgeChainHub, source *edgeChainSource, store *sqlite.Store) {
	t.Helper()
	board, session := "Feibot:U659", "100"
	if source.profile == "plate" {
		board, session = "plate-test", "plate-one"
	}
	c := newEdgeChainCentral(t, hub, board, session)
	c.waitRows(t, 3)
	before := c.snapshot(t, "snapshot")
	c.assertMetadata(t, store, before, 3)
	t.Logf("real central backlog drained: MySQL=%s PHP=%s migrations=%d; raw/results/member_results=3", before.MySQLVersion, before.PHPVersion, before.Migrations)

	// Already queued direct and relay copies must not duplicate raw facts,
	// projections or MySQL trigger-generated observation_created feed entries.
	c.snapshot(t, "revoke")
	number := int64(4)
	epc := "E2000004"
	if err := store.UpsertMember(t.Context(), domain.Member{ID: "4", EventID: "100", RaceID: "race", Number: &number, EPC: &epc}); err != nil {
		t.Fatal(err)
	}
	source.read(t, 4)
	source.waitACKs(t, 4, 4)
	assertEdgeChainRows(t, store, 4)
	// Hub ACK means Redis persistence, not central admission. Require repeated
	// actual consumer attempts while the PHP service's revocation is in force.
	edgeChainWait(t, "revoked source retained and retried by central consumer", func() bool {
		output := hub.docker(t, "exec", hub.id, "redis-cli", "--json", "XPENDING", "synthetic-edge-chain", "synthetic-central", "-", "+", "10")
		var entries [][]json.RawMessage
		if err := json.Unmarshal([]byte(output), &entries); err != nil || len(entries) == 0 || len(entries[0]) != 4 {
			return false
		}
		var attempts int
		return json.Unmarshal(entries[0][3], &attempts) == nil && attempts >= 2
	})
	rejected := c.snapshot(t, "snapshot")
	if !reflect.DeepEqual(before.Rows, rejected.Rows) || rejected.Results != 3 || rejected.MemberResults != 3 {
		t.Fatalf("revoked source changed central facts/projection: %+v", rejected)
	}
	publications := len(hub.entries(t))
	c.snapshot(t, "enable")
	c.waitRows(t, 4)
	final := c.snapshot(t, "snapshot")
	c.assertMetadata(t, store, final, 4)
	if !reflect.DeepEqual(final.Actions, []string{"grant", "revoke", "enable"}) {
		t.Fatalf("actual PHP service audit: %v", final.Actions)
	}
	if got := len(hub.entries(t)); got != publications {
		t.Fatalf("central recovery depended on a source resend: publications=%d want=%d", got, publications)
	}
	t.Log("revocation held the fourth packet pending; explicit re-enable drained it without source resend or metadata changes")
}

type edgeChainCentral struct {
	hub     *edgeChainHub
	phpID   string
	mysqlID string
	eventID string
	dbHost  string
}

type edgeChainCentralSnapshot struct {
	MySQLVersion     string           `json:"mysql_version"`
	PHPVersion       string           `json:"php_version"`
	Migrations       int              `json:"migrations"`
	Rows             []map[string]any `json:"rows"`
	Results          int              `json:"results"`
	MemberResults    int              `json:"member_results"`
	Finished         int              `json:"finished"`
	ResultRows       []map[string]any `json:"result_rows"`
	MemberResultRows []map[string]any `json:"member_result_rows"`
	FinishedRows     []map[string]any `json:"finished_rows"`
	BoardEvents      []map[string]any `json:"board_events"`
	Actions          []string         `json:"actions"`
	BindingEnabled   bool             `json:"binding_enabled"`
	SourceBindings   []map[string]any `json:"source_bindings"`
	SourceActions    []map[string]any `json:"source_actions"`
	Export           json.RawMessage  `json:"export"`
	Feed             struct {
		Items []struct {
			Type        string         `json:"type"`
			Observation map[string]any `json:"observation"`
		} `json:"items"`
		HasMore bool `json:"has_more"`
	} `json:"feed"`
}

func newEdgeChainCentral(t *testing.T, hub *edgeChainHub, board, session string, setupActions ...string) *edgeChainCentral {
	t.Helper()
	for _, key := range []string{"EDGE_MYSQL_IMAGE", "EDGE_PHP_IMAGE"} {
		if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(os.Getenv(key)) {
			t.Fatalf("%s must be an already installed immutable image ID", key)
		}
	}
	edgeChainBinary(t, "EDGE_SYNC_BINARY")
	root := os.Getenv("EDGE_RUN5_ROOT")
	if !filepath.IsAbs(root) {
		t.Fatal("EDGE_RUN5_ROOT must be an absolute RUN5 source checkout with installed vendor")
	}
	for _, path := range []string{"vendor/autoload.php", "tests/Support/edge-chain.php"} {
		if st, err := os.Stat(filepath.Join(root, path)); err != nil || !st.Mode().IsRegular() {
			t.Fatalf("missing RUN5 fixture dependency: %s", path)
		}
	}
	c := &edgeChainCentral{hub: hub, eventID: "100", dbHost: "127.0.0.1"}
	// Containers join the network-none Hub namespace: loopback only, no host
	// ports, bind mounts, image pulls or route to a production database.
	create := func(name, image string, command []string, args ...string) string {
		base := []string{"create", "--pull", "never", "--name", name + "-" + hub.id[:12], "--label", "task=CHR-SIDE-002", "--network", "container:" + hub.id,
			"--cpus", "1", "--memory", "768m", "--pids-limit", "128", "--security-opt", "no-new-privileges"}
		base = append(base, args...)
		base = append(base, image)
		id := hub.docker(t, append(base, command...)...)
		t.Cleanup(func() {
			if t.Failed() {
				logs, _ := edgeChainDocker("logs", "--tail", "40", id)
				t.Logf("%s: %s", name, logs)
			}
			if out, err := edgeChainDocker("rm", "--force", "--volumes", id); err != nil {
				t.Errorf("remove owned central fixture: %s %v", out, err)
			}
		})
		return id
	}
	mysqlID := create("chr-side-002-mysql", os.Getenv("EDGE_MYSQL_IMAGE"), []string{"--log-bin-trust-function-creators=1"}, "--tmpfs", "/var/lib/mysql:rw,nosuid,size=512m",
		"--env", "MYSQL_INITDB_SKIP_TZINFO=1", "--env", "MYSQL_ROOT_PASSWORD=synthetic-root", "--env", "MYSQL_DATABASE=synthetic_edge_chain", "--env", "MYSQL_USER=edge_fixture", "--env", "MYSQL_PASSWORD=synthetic-password")
	c.mysqlID = mysqlID
	hub.docker(t, "start", mysqlID)
	edgeChainWait(t, "MySQL fixture startup", func() bool {
		_, err := edgeChainDocker("exec", "--env", "MYSQL_PWD=synthetic-password", mysqlID, "mysql", "--protocol=TCP", "-h127.0.0.1", "-uedge_fixture", "synthetic_edge_chain", "-e", "SELECT 1")
		return err == nil
	})
	c.phpID = create("chr-side-002-php", os.Getenv("EDGE_PHP_IMAGE"), []string{"infinity"}, "--entrypoint", "/bin/sleep", "--workdir", "/fixture", "--cap-drop", "ALL",
		"--env", "EDGE_CHAIN_FIXTURE=synthetic-only", "--env", "APP_ENV=testing", "--env", "APP_KEY=base64:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		"--env", "APP_URL=http://localhost", "--env", "APP_TIMEZONE=UTC", "--env", "DB_CONNECTION=mysql", "--env", "DB_HOST=127.0.0.1",
		"--env", "DB_DATABASE=synthetic_edge_chain", "--env", "DB_USERNAME=edge_fixture", "--env", "DB_PASSWORD=synthetic-password",
		"--env", "CACHE_STORE=array", "--env", "CACHE_DRIVER=array", "--env", "SESSION_DRIVER=array", "--env", "QUEUE_CONNECTION=sync",
		"--env", "LOG_CHANNEL=stderr", "--env", "MAIL_MAILER=array", "--env", "TELESCOPE_ENABLED=false")
	hub.docker(t, "start", c.phpID)
	// Archive only tracked source, not .env*, cached config, logs or uploads.
	// The helper is copied explicitly to support review before its commit.
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	archive := exec.CommandContext(ctx, "git", "-C", root, "archive", "HEAD", "app", "bootstrap", "config", "database", "lang", "resources", "routes", "public/index.php", "artisan", "composer.json", "composer.lock")
	data, err := archive.Output()
	if err != nil {
		t.Fatalf("archive RUN5 tracked source: %v", err)
	}
	copyArchive := exec.CommandContext(ctx, "docker", "cp", "-", c.phpID+":/fixture")
	copyArchive.Stdin = bytes.NewReader(data)
	if output, err := copyArchive.CombinedOutput(); err != nil {
		t.Fatalf("copy RUN5 archive: %s %v", output, err)
	}
	hub.docker(t, "exec", c.phpID, "mkdir", "-p", "/fixture/tests/Support", "/fixture/storage/framework/cache", "/fixture/storage/framework/sessions", "/fixture/storage/framework/views", "/fixture/storage/logs")
	hub.docker(t, "cp", filepath.Join(root, "vendor"), c.phpID+":/fixture/vendor")
	hub.docker(t, "cp", filepath.Join(root, "tests/Support/edge-chain.php"), c.phpID+":/fixture/tests/Support/edge-chain.php")
	setupAction := "setup"
	if len(setupActions) == 1 && (setupActions[0] == "setup-raw" || setupActions[0] == "setup-auto") {
		setupAction = setupActions[0]
	} else if len(setupActions) != 0 {
		t.Fatal("unknown central fixture setup")
	}
	setup := c.snapshot(t, setupAction, board, session)
	t.Logf("RUN5 schema/grant ready: MySQL=%s PHP=%s migrations=%d", setup.MySQLVersion, setup.PHPVersion, setup.Migrations)
	c.startConsumer(t)
	return c
}

func (c *edgeChainCentral) startConsumer(t *testing.T) {
	t.Helper()
	hub := c.hub
	binary := edgeChainBinary(t, "EDGE_SYNC_BINARY")
	hub.docker(t, "cp", binary, hub.id+":/tmp/rfid-sync")
	hub.docker(t, "exec", "-d", "--env", "DB_HOST="+c.dbHost, "--env", "DB_DATABASE=synthetic_edge_chain", "--env", "DB_USERNAME=edge_fixture", "--env", "DB_PASSWORD=synthetic-password",
		"--env", "REDIS_ADDR=127.0.0.1:6379", "--env", "REDIS_STREAM=synthetic-edge-chain", "--env", "REDIS_GROUP=synthetic-central", "--env", "REDIS_CONSUMER=fixture",
		"--env", "REDIS_BLOCK=1s", "--env", "REDIS_CLAIM_IDLE=1s", "--env", "WORKERS=1", "--env", "METRICS_LOG_INTERVAL=0s",
		hub.id, "/bin/sh", "-c", "exec /tmp/rfid-sync > /tmp/sync.log 2>&1")
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := edgeChainDocker("exec", hub.id, "tail", "-60", "/tmp/sync.log")
			t.Logf("central consumer: %s", out)
		}
	})
}

func (c *edgeChainCentral) snapshot(t *testing.T, action string, args ...string) edgeChainCentralSnapshot {
	t.Helper()
	if action == "snapshot" && c.eventID == "200" {
		action, args = "snapshot-event", []string{"200"}
	}
	output := c.hub.docker(t, append([]string{"exec", c.phpID, "php", "tests/Support/edge-chain.php", action}, args...)...)
	var snapshot edgeChainCentralSnapshot
	decoder := json.NewDecoder(strings.NewReader(output))
	decoder.UseNumber()
	if err := decoder.Decode(&snapshot); err != nil {
		t.Fatalf("PHP fixture %s: %s: %v", action, output, err)
	}
	return snapshot
}

func (c *edgeChainCentral) waitRows(t *testing.T, want int) {
	t.Helper()
	c.waitRowsAndOutcomes(t, want, want)
}

func (c *edgeChainCentral) waitRawRows(t *testing.T, want int) {
	t.Helper()
	c.waitRowsAndOutcomes(t, want, 0)
}

func (c *edgeChainCentral) waitRowsAndOutcomes(t *testing.T, want, outcomes int) {
	t.Helper()
	edgeChainWait(t, fmt.Sprintf("central MySQL facts/projection=%d and Redis pending=0", want), func() bool {
		s := c.snapshot(t, "snapshot")
		if len(s.Rows) != want || s.Results != outcomes || s.MemberResults != outcomes || s.Finished != outcomes {
			return false
		}
		// Pending=0 alone can hide entries the consumer has not read yet.
		// This fixture creates exactly one group with whitespace-free fields.
		output := c.hub.docker(t, "exec", c.hub.id, "redis-cli", "--raw", "XINFO", "GROUPS", "synthetic-edge-chain")
		fields := strings.Fields(output)
		if len(fields)%2 != 0 {
			return false
		}
		group := make(map[string]string)
		for i := 0; i < len(fields); i += 2 {
			group[fields[i]] = fields[i+1]
		}
		return group["name"] == "synthetic-central" && group["pending"] == "0" && group["lag"] == "0"
	})
}

func (c *edgeChainCentral) assertMetadata(t *testing.T, store *sqlite.Store, snapshot edgeChainCentralSnapshot, want int) {
	t.Helper()
	journal, err := store.EdgeJournal(t.Context(), c.eventID, 0, 10)
	if err != nil || len(journal) != want || len(snapshot.Rows) != want || len(snapshot.Feed.Items) != want || snapshot.Feed.HasMore {
		t.Fatalf("central/Desk/feed counts differ: %d %d %d %v", len(journal), len(snapshot.Rows), len(snapshot.Feed.Items), err)
	}
	expected := make(map[string]map[string]any)
	for _, item := range journal {
		p, err := edge.Decode(item.Payload)
		if err != nil {
			t.Fatal(err)
		}
		expected[p.ID] = map[string]any{"id": p.ID, "event_id": p.ExternalEventID, "board": p.Board, "epc": p.EPC, "time": p.Time, "ant": p.Ant, "number": p.Number, "status": p.Status, "rssi": p.RSSI,
			"observation_version": 1, "capture_source_id": p.CaptureSourceID, "origin_system": p.OriginSystem, "origin_instance_id": p.OriginInstanceID, "origin_sequence": p.OriginSequence,
			"edge_version": 1, "source_session_id": p.SourceSessionID, "identity_profile": p.IdentityProfile, "clock_evidence_id": p.ClockEvidenceID, "clock_quality": p.ClockQuality}
	}
	check := func(rows []map[string]any) {
		t.Helper()
		if len(rows) != want {
			t.Fatalf("metadata row count=%d want=%d", len(rows), want)
		}
		seen := make(map[string]bool)
		for _, row := range rows {
			id := fmt.Sprint(row["id"])
			packet, ok := expected[id]
			if !ok || seen[id] {
				t.Fatalf("unexpected or repeated central identity %s", id)
			}
			seen[id] = true
			for key, value := range packet {
				if fmt.Sprint(row[key]) != fmt.Sprint(value) {
					t.Fatalf("central/export/feed %s.%s=%v want=%v", id, key, row[key], value)
				}
			}
		}
	}
	check(snapshot.Rows)
	feedRows := make([]map[string]any, 0, want)
	for _, item := range snapshot.Feed.Items {
		if item.Type != "observation_created" {
			t.Fatalf("unexpected feed change: %s", item.Type)
		}
		item.Observation["time"] = item.Observation["time_ms"]
		feedRows = append(feedRows, item.Observation)
	}
	check(feedRows)
	var exported struct {
		Rows []map[string]any `json:"rfid_logs"`
	}
	decoder := json.NewDecoder(bytes.NewReader(snapshot.Export))
	decoder.UseNumber()
	if err := decoder.Decode(&exported); err != nil {
		t.Fatal(err)
	}
	check(exported.Rows)
	// Exercise Desk's real export importer with PHP's actual document, not a
	// hand-written equivalent. Imported rows must not create either outbox.
	var document EventExport
	if err := json.Unmarshal(snapshot.Export, &document); err != nil {
		t.Fatal(err)
	}
	catalog, err := sqlite.NewEventCatalog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	imported, err := catalog.OpenOrCreate(c.eventID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = imported.Close() })
	if _, err := NewEventImporter(imported).Import(t.Context(), &document); err != nil {
		t.Fatalf("actual PHP export into Desk: %v", err)
	}
	var raw, native, owned int
	if err := imported.DB().QueryRow(`SELECT (SELECT COUNT(*) FROM rfid_logs), (SELECT COUNT(*) FROM observation_outbox), (SELECT COUNT(*) FROM edge_observation_outbox)`).Scan(&raw, &native, &owned); err != nil || raw != want || native != 0 || owned != 0 {
		t.Fatalf("import echo: raw=%d native=%d edge=%d %v", raw, native, owned, err)
	}
	originalLogs, err := store.ListRfidLogs(t.Context(), c.eventID)
	if err != nil {
		t.Fatal(err)
	}
	importedLogs, err := imported.ListRfidLogs(t.Context(), c.eventID)
	if err != nil || !reflect.DeepEqual(originalLogs, importedLogs) {
		t.Fatalf("PHP export/Desk import changed original stored observations: %v", err)
	}
}
