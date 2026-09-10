package service

import (
	"bufio"
	"context"
	"io"
	"log"
	"net"
	"strings"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/rfid-core/edge"
	"gitlab.com/fightmaster1/rfid-core/ingest"
)

func relayEventFixture(t *testing.T) (string, *EventService, *sqlite.Store, ingest.Event) {
	t.Helper()
	dir := t.TempDir()
	catalog, err := sqlite.NewEventCatalog(dir)
	if err != nil {
		t.Fatal(err)
	}
	events := NewEventService(catalog, log.New(io.Discard, "", 0))
	t.Cleanup(events.Close)
	store, err := catalog.OpenOrCreate("100")
	if err != nil {
		t.Fatal(err)
	}
	packet := seedEdgeLiveFixture(t, store)
	publisher := &edgePublisher{store: store, eventID: "100", stats: &LiveStats{}, logger: log.New(io.Discard, "", 0)}
	if err := publisher.Publish(context.Background(), packet); err != nil {
		t.Fatal(err)
	}
	return dir, events, store, packet
}

func relayPeer(t *testing.T, reply func(ingest.Event, int) []byte) (string, <-chan []byte) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	seen := make(chan []byte, 32)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for attempt := 0; ; attempt++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			line, err := bufio.NewReader(conn).ReadBytes('\n')
			if err == nil {
				payload := line[:len(line)-1]
				packet, decodeErr := edge.Decode(payload)
				if decodeErr != nil {
					t.Errorf("relay sent invalid wire: %v", decodeErr)
				} else {
					seen <- payload
					if ack := reply(packet, attempt); ack != nil {
						_, _ = conn.Write(ack)
					}
				}
			}
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	return listener.Addr().String(), seen
}

func waitRelayProgress(t *testing.T, store *sqlite.Store, check func(sqlite.EdgeRelayProgress) bool) sqlite.EdgeRelayProgress {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last sqlite.EdgeRelayProgress
	for time.Now().Before(deadline) {
		var err error
		last, err = store.EdgeRelayProgress(context.Background(), "100")
		if err != nil {
			t.Fatal(err)
		}
		if check(last) {
			return last
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("relay did not reach expected state: %+v", last)
	return last
}

func TestEdgeRelayRestartResendsLostACKWithoutInputOrSiteSync(t *testing.T) {
	dir, events, store, packet := relayEventFixture(t)
	endpoint, seen := relayPeer(t, func(event ingest.Event, attempt int) []byte {
		if attempt == 0 {
			return nil // persisted at the fake peer, but its ACK was lost
		}
		return edge.ACK(event)
	})
	logger := log.New(io.Discard, "", 0)
	manager := NewEdgeRelayManager(events, logger, 5*time.Millisecond)
	t.Cleanup(manager.StopAll)
	if _, err := manager.Configure(context.Background(), "100", domain.EdgeRelayConfig{Endpoint: endpoint, Enabled: true}, true); err != nil {
		t.Fatal(err)
	}
	waitRelayProgress(t, store, func(p sqlite.EdgeRelayProgress) bool { return p.Pending == 1 && p.LastError != "" })
	manager.StopAll()
	events.Close()
	reopened, err := NewEventManager(dir, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	store, err = reopened.Open("100")
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewEdgeRelayManager(reopened, logger, 5*time.Millisecond)
	t.Cleanup(restarted.StopAll)
	if err := restarted.ResumeConfigured(context.Background()); err != nil {
		t.Fatal(err)
	}
	progress := waitRelayProgress(t, store, func(p sqlite.EdgeRelayProgress) bool { return p.Acked == 1 })
	if progress.Attempts != 2 || progress.Pending != 0 {
		t.Fatalf("restart delivery: %+v", progress)
	}
	want, _ := edge.Encode(packet)
	for i := 0; i < 2; i++ {
		if got := <-seen; string(got) != string(want) {
			t.Fatal("restart changed source event/session/clock/wire")
		}
	}
	journal, err := store.EdgeJournal(context.Background(), "100", 0, 10)
	if err != nil || len(journal) != 1 || journal[0].State != "acked" || string(journal[0].Payload) != string(want) {
		t.Fatalf("journal changed: %+v %v", journal, err)
	}
	var nativeCount, resultCount int
	if err := store.DB().QueryRow(`SELECT (SELECT COUNT(*) FROM observation_outbox),(SELECT COUNT(*) FROM results)`).Scan(&nativeCount, &resultCount); err != nil || nativeCount != 0 || resultCount != 1 {
		t.Fatalf("relay claimed ownership or reprojected: native=%d results=%d %v", nativeCount, resultCount, err)
	}
	restarted.StopAll()
	third := NewEdgeRelayManager(reopened, logger, 5*time.Millisecond)
	t.Cleanup(third.StopAll)
	if err := third.ResumeConfigured(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-seen:
		t.Fatal("ACKed row resent after another restart")
	case <-time.After(60 * time.Millisecond):
	}
}

func TestEdgeRelayWrongScopedACKStaysPendingAndConfirmedRedirectKeepsWire(t *testing.T) {
	_, events, store, packet := relayEventFixture(t)
	wrong, firstSeen := relayPeer(t, func(event ingest.Event, _ int) []byte {
		event.ExternalEventID = 999
		return edge.ACK(event)
	})
	good, secondSeen := relayPeer(t, func(event ingest.Event, _ int) []byte { return edge.ACK(event) })
	manager := NewEdgeRelayManager(events, log.New(io.Discard, "", 0), 5*time.Millisecond)
	t.Cleanup(manager.StopAll)
	config, err := manager.Configure(context.Background(), "100", domain.EdgeRelayConfig{Endpoint: wrong, Enabled: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	waitRelayProgress(t, store, func(p sqlite.EdgeRelayProgress) bool { return p.LastError != "" && p.Pending == 1 && p.Acked == 0 })
	config.Endpoint = good
	if _, err := manager.Configure(context.Background(), "100", config, false); err == nil {
		t.Fatal("pending queue redirected without confirmation")
	}
	if running, _ := manager.Status("100"); !running {
		t.Fatal("failed config command stopped enabled queue")
	}
	if _, err := manager.Configure(context.Background(), "100", config, true); err != nil {
		t.Fatal(err)
	}
	waitRelayProgress(t, store, func(p sqlite.EdgeRelayProgress) bool { return p.Acked == 1 })
	want, _ := edge.Encode(packet)
	if string(<-firstSeen) != string(want) || string(<-secondSeen) != string(want) {
		t.Fatal("explicit target change altered original source packet")
	}
}

func TestEdgeRelayPausePersistsAndDoesNotStartInput(t *testing.T) {
	_, events, store, _ := relayEventFixture(t)
	endpoint, seen := relayPeer(t, func(event ingest.Event, _ int) []byte { return edge.ACK(event) })
	logger := log.New(io.Discard, "", 0)
	manager := NewEdgeRelayManager(events, logger, 5*time.Millisecond)
	config, err := manager.Configure(context.Background(), "100", domain.EdgeRelayConfig{Endpoint: endpoint, Enabled: false}, true)
	if err != nil {
		t.Fatal(err)
	}
	manager.StopAll()
	restarted := NewEdgeRelayManager(events, logger, 5*time.Millisecond)
	t.Cleanup(restarted.StopAll)
	if err := restarted.ResumeConfigured(context.Background()); err != nil {
		t.Fatal(err)
	}
	if running, _ := restarted.Status("100"); running {
		t.Fatal("restart enabled paused relay")
	}
	if progress, _ := store.EdgeRelayProgress(context.Background(), "100"); progress.Attempts != 0 || progress.Pending != 1 {
		t.Fatalf("pause changed queue: %+v", progress)
	}
	config.Enabled = true
	if _, err := restarted.Configure(context.Background(), "100", config, false); err != nil {
		t.Fatal(err)
	}
	waitRelayProgress(t, store, func(p sqlite.EdgeRelayProgress) bool { return p.Acked == 1 })
	<-seen
}

func TestEdgeRelayCorruptJournalIsNotSentOrDowngraded(t *testing.T) {
	_, events, store, packet := relayEventFixture(t)
	endpoint, seen := relayPeer(t, func(event ingest.Event, _ int) []byte { return edge.ACK(event) })
	payload, _ := edge.Encode(packet)
	corrupt := strings.Replace(string(payload), `"external_event_id":100`, `"external_event_id":999`, 1)
	if _, err := store.DB().Exec(`UPDATE edge_observation_outbox SET payload_json=?`, corrupt); err != nil {
		t.Fatal(err)
	}
	manager := NewEdgeRelayManager(events, log.New(io.Discard, "", 0), 5*time.Millisecond)
	t.Cleanup(manager.StopAll)
	if _, err := manager.Configure(context.Background(), "100", domain.EdgeRelayConfig{Endpoint: endpoint, Enabled: true}, true); err != nil {
		t.Fatal(err)
	}
	waitRelayProgress(t, store, func(p sqlite.EdgeRelayProgress) bool { return p.Pending == 1 && p.LastError != "" })
	select {
	case <-seen:
		t.Fatal("journal/event mismatch sent")
	default:
	}
}
