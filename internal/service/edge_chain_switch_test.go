//go:build linux && edgeintegration

package service

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/rfid-core/edge"
)

func TestEdgeChainEventSwitchPreservesHeldBacklog(t *testing.T) {
	for _, profile := range []string{"feibot", "plate"} {
		t.Run(profile, func(t *testing.T) {
			board, oldSession := "Feibot:U659", "100"
			if profile == "plate" {
				board, oldSession = "plate-test", "plate-one"
			}
			oldHub := newEdgeChainHub(t, board, oldSession)
			catalog, err := sqlite.NewEventCatalog(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			oldStore := edgeSwitchStore(t, catalog, "100", board, oldSession)
			live := NewLiveManager(log.New(io.Discard, "", 0))
			t.Cleanup(live.StopAll)
			port := edgeTestPort(t)
			startDesk := func(store *sqlite.Store, eventID string) {
				t.Helper()
				if err := live.StartEdge(store, eventID, port); err != nil {
					t.Fatal(err)
				}
			}
			startDesk(oldStore, "100")
			source := newEdgeChainSource(t, profile, oldHub.endpoint, "127.0.0.1:"+port)
			source.start(t)
			if profile == "plate" {
				source.confirmClock(t)
			}
			source.read(t, 1)
			source.waitACKs(t, 1, 1)
			assertEdgeChainRows(t, oldStore, 1)
			acknowledged := edgeSwitchDeliveries(t, source)

			// Both receivers go away before the second observation. Preserve the
			// actual source wire and pending state before any event settings change.
			live.StopEdge("100")
			oldHub.docker(t, "pause", oldHub.id)
			source.read(t, 2)
			source.waitRetry(t, "hub")
			source.waitRetry(t, "chrono")
			oldRows := edgeSwitchRows(t, source)
			if len(oldRows) != 2 {
				t.Fatalf("old source rows=%d", len(oldRows))
			}

			newSession := "200"
			if profile == "plate" {
				source.switchAction(t, "/actions", url.Values{"action": {"pause"}})
				// The local form allocates the session; never fabricate one in DB.
				source.plateSwitchSettings(t, "200", oldHub.endpoint, "127.0.0.1:"+port)
				newSession = source.switchStatus(t).Settings.Source.SessionID
				if newSession == "" || newSession == oldSession {
					t.Fatal("new plate binding did not receive a new session")
				}
			}
			newHub := newEdgeChainHubForEvent(t, 200, board, newSession)
			if profile == "plate" {
				source.plateSwitchSettings(t, "200", newHub.endpoint, "127.0.0.1:"+port)
			} else {
				source.selectFeibotFixture(t, "200", newHub.endpoint)
			}
			newStore := edgeSwitchStore(t, catalog, "200", board, newSession)
			// Also stop before the later-created store's cleanup on failures.
			t.Cleanup(live.StopAll)
			startDesk(newStore, "200") // the very same network address now owns event 200
			if profile == "plate" {
				source.confirmClock(t)
			}
			source.read(t, 3)
			if profile == "feibot" {
				edgeChainWait(t, "fresh CSV activates event 200", func() bool { return source.switchStatus(t).Event.FeibotEventID == "200" })
			}
			source.waitACKs(t, 1, 1) // status is scoped to the selected event
			source.assertHeld(t, 2)
			assertEdgeChainRows(t, oldStore, 1)
			assertEdgeChainRows(t, newStore, 1)
			allRows := edgeSwitchRows(t, source)
			if len(allRows) != 3 {
				t.Fatalf("source rows after switch=%d", len(allRows))
			}
			for id, row := range oldRows {
				if allRows[id] != row {
					t.Fatalf("switch rewrote old observation %s", id)
				}
			}
			publications := len(newHub.entries(t))

			// Restore the old receiver, but keep the application's selection on
			// event 200. Neither restart nor receiver availability releases history.
			oldHub.docker(t, "unpause", oldHub.id)
			source.stop(t)
			source.start(t)
			source.waitACKs(t, 1, 1)
			source.assertHeld(t, 2)
			if !reflect.DeepEqual(allRows, edgeSwitchRows(t, source)) {
				t.Fatal("restart rewrote or dropped source observations")
			}
			if len(newHub.entries(t)) != publications {
				t.Fatal("restart repeated already ACKed new-event packets")
			}
			t.Log("event 200 progressed while event 100 backlog remained unchanged and held across restart")

			// Deliberately restore history while both target addresses still
			// accept event 200. Actual Hub and Desk must reject, not reassign it.
			beforeWrongTarget := edgeSwitchDeliveries(t, source)
			if profile == "plate" {
				source.switchAction(t, "/actions", url.Values{"action": {"pause"}})
				source.switchAction(t, "/actions", url.Values{"action": {"select_session"}, "session_id": {oldSession}})
			} else {
				source.selectFeibotFixture(t, "100", newHub.endpoint)
				// Maintenance Start is the existing explicit recovery operation;
				// there is no fabricated CSV activity or console command.
				source.switchAction(t, "/api/start", url.Values{})
			}
			edgeChainWait(t, "both wrong-event receivers actually attempted", func() bool {
				now := edgeSwitchDeliveries(t, source)
				attempted := 0
				for key, before := range beforeWrongTarget {
					if before.State != "acknowledged" && now[key].Attempts > before.Attempts && now[key].Ack == "" {
						attempted++
					}
				}
				return attempted == 2
			})
			assertEdgeChainRows(t, newStore, 1)
			if len(newHub.entries(t)) != publications {
				t.Fatal("Hub admitted an old packet under the new-event listener")
			}
			if logs := newHub.docker(t, "logs", "--tail", "40", newHub.id); !strings.Contains(logs, "edge board/event/session is not provisioned") {
				t.Fatal("Hub did not observe an actual binding rejection")
			}
			if live.Status("200").Edge.Errors == 0 {
				t.Fatal("Desk did not observe the wrong-event rejection")
			}

			// Correct the receiving configuration, not stored observations. Old
			// timestamps already exist: no new reading/date confirmation is needed.
			live.StopEdge("200")
			startDesk(oldStore, "100")
			if profile == "plate" {
				source.plateSwitchSettings(t, "100", oldHub.endpoint, "127.0.0.1:"+port)
			} else {
				source.switchAction(t, "/api/config", url.Values{"destination_event_id": {"100"}, "timezone": {"UTC"}, "wire_protocol": {edge.Protocol}, "enabled_hub": {"on"}, "enabled_chrono": {"on"}, "endpoint_hub": {"tcp://" + oldHub.endpoint}})
			}
			source.waitACKs(t, 2, 2)
			source.assertHeld(t, 0)
			assertEdgeChainRows(t, oldStore, 2)
			assertEdgeChainRows(t, newStore, 1)
			if !reflect.DeepEqual(allRows, edgeSwitchRows(t, source)) {
				t.Fatal("recovery changed immutable source rows")
			}
			finalDeliveries := edgeSwitchDeliveries(t, source)
			for key, before := range acknowledged {
				if finalDeliveries[key] != before {
					t.Fatalf("already ACKed delivery changed: %s", key)
				}
			}
			for key, d := range finalDeliveries {
				if d.State != "acknowledged" || d.Ack == "" {
					t.Fatalf("delivery did not recover: %s %+v", key, d)
				}
			}
			for _, pair := range []struct {
				hub   *edgeChainHub
				store *sqlite.Store
				event string
			}{{oldHub, oldStore, "100"}, {newHub, newStore, "200"}} {
				journal, err := pair.store.EdgeJournal(t.Context(), pair.event, 0, 10)
				if err != nil {
					t.Fatal(err)
				}
				for _, item := range journal {
					if string(item.Payload) != allRows[item.ObservationID].Payload {
						t.Fatal("Desk rewrote the restored source payload")
					}
				}
				for _, entry := range pair.hub.entries(t) {
					p, err := edge.Decode([]byte(allRows[entry["id"]].Payload))
					if err != nil || fmt.Sprint(p.ExternalEventID) != pair.event || entry["external_event_id"] != pair.event || entry["source_session_id"] != p.SourceSessionID || entry["origin_instance_id"] != p.OriginInstanceID || entry["clock_evidence_id"] != p.ClockEvidenceID {
						t.Fatalf("Hub binding/origin/clock changed: %v %v", entry, err)
					}
				}
			}
			t.Log("wrong-event targets rejected history; explicit recovery drained only original event/session without rewriting or rereading")
		})
	}
}

func edgeSwitchStore(t *testing.T, catalog *sqlite.EventCatalog, eventID, board, session string) *sqlite.Store {
	t.Helper()
	store, err := catalog.OpenOrCreate(eventID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	start := time.Now().Add(-time.Minute).UnixMilli()
	for _, err := range []error{
		store.UpsertEvent(t.Context(), domain.Event{ID: eventID, Name: "Synthetic switch " + eventID, Timezone: "UTC"}),
		store.UpsertRace(t.Context(), domain.Race{ID: "race", EventID: eventID, Name: "Race", StartedAtMs: &start}),
		store.UpsertCheckpoint(t.Context(), domain.Checkpoint{ID: "finish", EventID: eventID, RaceID: "race", Board: board, Type: domain.CheckpointType(3), Sort: 1}),
		store.SetEdgeBindings(t.Context(), eventID, []domain.EdgeBinding{{Board: board, SourceSessionID: session}}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	for n := 1; n <= 3; n++ {
		number, epc := int64(n), fmt.Sprintf("E200%04d", n)
		if err := store.UpsertMember(t.Context(), domain.Member{ID: strconv.Itoa(n), EventID: eventID, RaceID: "race", Number: &number, EPC: &epc}); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

type edgeSwitchStatus struct {
	Revision     int64                           `json:"revision"`
	Paused       bool                            `json:"paused"`
	Event        struct{ FeibotEventID string }  `json:"event"`
	PendingEvent *struct{ FeibotEventID string } `json:"pending_event"`
	Held         int                             `json:"held_other_event_deliveries"`
	Journal      struct {
		Held int `json:"held_deliveries"`
	} `json:"journal"`
	Settings struct {
		Source struct {
			SessionID string `json:"session_id"`
		} `json:"source"`
	} `json:"settings"`
}

func (s *edgeChainSource) switchStatus(t *testing.T) edgeSwitchStatus {
	t.Helper()
	data, code, err := s.request(http.MethodGet, "/api/status", nil)
	var status edgeSwitchStatus
	if err != nil || code != 200 {
		t.Fatalf("switch status %d: %s %v", code, data, err)
	}
	if err := json.Unmarshal(data, &status); err != nil {
		t.Fatal(err)
	}
	return status
}

func (s *edgeChainSource) assertHeld(t *testing.T, want int) {
	t.Helper()
	st := s.switchStatus(t)
	got := st.Held
	if s.profile == "plate" {
		got = st.Journal.Held
	}
	if got != want {
		t.Fatalf("held deliveries=%d want=%d", got, want)
	}
}

func (s *edgeChainSource) switchAction(t *testing.T, path string, form url.Values) {
	t.Helper()
	page, code, err := s.request(http.MethodGet, "/", nil)
	if err != nil || code != 200 {
		t.Fatalf("local form: %d %v", code, err)
	}
	match := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindSubmatch(page)
	if len(match) != 2 {
		t.Fatal("local CSRF field missing")
	}
	form.Set("csrf_token", string(match[1]))
	if s.profile == "plate" {
		form.Set("revision", strconv.FormatInt(s.switchStatus(t).Revision, 10))
	}
	data, code, err := s.request(http.MethodPost, path, form)
	if err != nil || code != http.StatusSeeOther {
		t.Fatalf("local %s: %d %s %v", path, code, data, err)
	}
}

func (s *edgeChainSource) plateSwitchSettings(t *testing.T, eventID, hub, desk string) {
	t.Helper()
	s.switchAction(t, "/actions", url.Values{"action": {"configure"}, "event_id": {eventID}, "board": {"plate-test"}, "device_code": {"plate-test"}, "timezone": {"UTC"}, "reader_address": {""}, "endpoint_hub": {"tcp://" + hub}, "endpoint_chrono": {"tcp://" + desk}, "enabled_hub": {"yes"}, "enabled_chrono": {"yes"}, "past_seconds": {"3600"}, "future_seconds": {"60"}, "max_age_seconds": {"3600"}, "max_step_ms": {"1000"}})
}

func (s *edgeChainSource) selectFeibotFixture(t *testing.T, eventID, hub string) {
	t.Helper()
	data, err := os.ReadFile(s.config)
	if err != nil {
		t.Fatal(err)
	}
	nextPath := filepath.Join(s.dir, "csv-"+eventID)
	if eventID == "100" {
		nextPath = filepath.Join(s.dir, "csv")
	}
	if err := os.MkdirAll(nextPath, 0o700); err != nil {
		t.Fatal(err)
	}
	// Synthetic effective INI only: no real vendor file or UI is modified.
	text := string(data)
	for key, value := range map[string]string{"feibot_event_id": eventID, "destination_event_id": eventID, "csv_path": nextPath} {
		re := regexp.MustCompile(`(?m)^` + key + ` = [^\n]*$`)
		if len(re.FindAllString(text, -1)) != 1 {
			t.Fatalf("ambiguous fixture setting %s", key)
		}
		text = re.ReplaceAllStringFunc(text, func(string) string { return key + " = " + value })
	}
	start := strings.Index(text, "[destination.hub]\n")
	if start < 0 {
		t.Fatal("fixture Hub section missing")
	}
	endOffset := strings.Index(text[start:], "[destination.chrono]\n")
	if endOffset < 0 {
		t.Fatal("fixture Desk section missing")
	}
	end := endOffset + start
	section := text[start:end]
	re := regexp.MustCompile(`(?m)^endpoint = [^\n]*$`)
	section = re.ReplaceAllStringFunc(section, func(string) string { return "endpoint = tcp://" + hub })
	text = text[:start] + section + text[end:]
	temporary := filepath.Join(s.dir, "next-fixture-config")
	if err := os.WriteFile(temporary, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporary, s.config); err != nil {
		t.Fatal(err)
	}
	edgeChainWait(t, "Feibot selection observed before fresh CSV activity", func() bool {
		st := s.switchStatus(t)
		return st.PendingEvent != nil && st.PendingEvent.FeibotEventID == eventID
	})
	s.csvPath, s.eventID = nextPath, eventID
}

type edgeSwitchRow struct{ Payload, EventID, Raw, AcceptedAt string }
type edgeSwitchDelivery struct {
	Attempts   int
	State, Ack string
}

func edgeSwitchDB(t *testing.T, source *edgeChainSource) *sql.DB {
	t.Helper()
	uri := url.URL{Scheme: "file", Path: filepath.Join(source.dir, "sidecar.db"), RawQuery: "mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(3000)"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	return db
}

func edgeSwitchRows(t *testing.T, source *edgeChainSource) map[string]edgeSwitchRow {
	t.Helper()
	db := edgeSwitchDB(t, source)
	defer db.Close()
	rows, err := db.QueryContext(t.Context(), `SELECT id, payload, destination_event_id, raw, accepted_at FROM reads`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := make(map[string]edgeSwitchRow)
	for rows.Next() {
		var id string
		var row edgeSwitchRow
		if err := rows.Scan(&id, &row.Payload, &row.EventID, &row.Raw, &row.AcceptedAt); err != nil {
			t.Fatal(err)
		}
		out[id] = row
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func edgeSwitchDeliveries(t *testing.T, source *edgeChainSource) map[string]edgeSwitchDelivery {
	t.Helper()
	db := edgeSwitchDB(t, source)
	defer db.Close()
	rows, err := db.QueryContext(t.Context(), `SELECT read_id, destination_id, attempts, state, COALESCE(acknowledged_at,'') FROM deliveries`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := make(map[string]edgeSwitchDelivery)
	for rows.Next() {
		var id, destination string
		var row edgeSwitchDelivery
		if err := rows.Scan(&id, &destination, &row.Attempts, &row.State, &row.Ack); err != nil {
			t.Fatal(err)
		}
		out[id+":"+destination] = row
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
