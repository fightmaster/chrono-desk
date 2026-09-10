package service

import (
	"bufio"
	"context"
	"fmt"
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
	"gitlab.com/fightmaster1/rfid-core/tcp"
)

func edgeLiveFixture(t *testing.T) (*sqlite.Store, ingest.Event) {
	t.Helper()
	store := newTestStore(t)
	ctx := context.Background()
	instant := time.Date(2026, 9, 10, 8, 20, 0, 0, time.UTC)
	start := instant.Add(-20 * time.Minute).UnixMilli()
	epc := "E280AABB"
	for _, err := range []error{
		store.UpsertEvent(ctx, domain.Event{ID: "100", Name: "Synthetic edge live", Timezone: "UTC"}),
		store.UpsertRace(ctx, domain.Race{ID: "race", EventID: "100", Name: "Race", StartedAtMs: &start}),
		store.UpsertCheckpoint(ctx, domain.Checkpoint{ID: "finish", EventID: "100", RaceID: "race", Board: "Feibot:U659", Type: domain.CheckpointType(3), Sort: 1}),
		store.UpsertMember(ctx, domain.Member{ID: "member", EventID: "100", RaceID: "race", EPC: &epc}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetEdgeBindings(ctx, "100", []domain.EdgeBinding{{Board: "Feibot:U659", SourceSessionID: "session-one"}}); err != nil {
		t.Fatal(err)
	}
	event := ingest.Event{EdgeVersion: 1, ExternalEventID: 100, SourceSessionID: "session-one", IdentityProfile: edge.IdentityRFID, ClockEvidenceID: "source-clock", ClockQuality: edge.ClockSource,
		Board: "Feibot:U659", EPC: "E280AABB", Time: instant.UnixMilli(), RTC: instant.Format(time.RFC3339Nano), Ant: 1, RSSI: -50,
		ObservationVersion: 1, CaptureSourceID: "edge:U659:session-one", OriginSystem: "feibot-sidecar", OriginInstanceID: "sidecar-one", OriginSequence: 42}
	event.ID = ingest.RFIDReadID(event.Board, event.EPC, event.Time, event.Ant)
	return store, event
}

type edgeAckConn struct {
	net.Conn
	write func([]byte) (int, error)
}

func (c edgeAckConn) Write(p []byte) (int, error) { return c.write(p) }

func edgeRoundTrip(t *testing.T, store *sqlite.Store, event ingest.Event, failACK bool) (string, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	publisher := &edgePublisher{store: store, eventID: "100", stats: &LiveStats{}, logger: log.New(io.Discard, "", 0)}
	pipeline := ingest.NewPipeline(publisher, 1, 4, time.Second)
	defer pipeline.Close()
	server, client := net.Pipe()
	defer client.Close()
	wrapped := edgeAckConn{Conn: server, write: func(p []byte) (int, error) {
		// An ACK is allowed only after the real SQLite transaction and projection.
		var raw, outbox, results int
		if err := store.DB().QueryRow(`SELECT (SELECT COUNT(*) FROM rfid_logs),(SELECT COUNT(*) FROM edge_observation_outbox),(SELECT COUNT(*) FROM results)`).Scan(&raw, &outbox, &results); err != nil {
			t.Error(err)
		}
		if raw != 1 || outbox != 1 || results != 1 {
			t.Errorf("ACK before committed state: raw=%d outbox=%d results=%d", raw, outbox, results)
		}
		if failACK {
			_ = server.Close()
			return 0, io.ErrClosedPipe
		}
		return server.Write(p)
	}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		tcp.HandleConnWithContext(ctx, wrapped, tcp.ListenerConfig{Name: "actual-desk-edge", Adapter: tcp.EdgeAdapter{}, AckMode: tcp.AckModeID, ReadTimeout: time.Second, WriteTimeout: time.Second, MaxLineLenBytes: edge.MaxFrameBytes}, pipeline)
	}()
	payload, err := edge.Encode(event)
	if err != nil {
		t.Fatal(err)
	}
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := client.Write(append(payload, '\n')); err != nil {
		t.Fatal(err)
	}
	ack, err := bufio.NewReader(client).ReadString('\n')
	_ = client.Close()
	cancel()
	<-done
	return ack, err
}

func TestEdgeLiveCommitsBeforeACKAndRetriesWithoutNewOwnership(t *testing.T) {
	store, event := edgeLiveFixture(t)
	ctx := context.Background()
	if ack, err := edgeRoundTrip(t, store, event, true); err == nil || ack != "" {
		t.Fatal("injected ACK loss not observed")
	}
	if ack, err := edgeRoundTrip(t, store, event, false); err != nil || ack != string(edge.ACK(event)) {
		t.Fatalf("retry ACK=%q err=%v", ack, err)
	}
	items, err := store.EdgeJournal(ctx, "100", 0, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("edge journal=%+v %v", items, err)
	}
	want, _ := edge.Encode(event)
	if string(items[0].Payload) != string(want) {
		t.Fatal("clock/session/origin wire changed")
	}
	var origin string
	var seq, finish int64
	if err := store.DB().QueryRow(`SELECT origin_instance_id,origin_sequence FROM rfid_logs WHERE id=?`, event.ID).Scan(&origin, &seq); err != nil {
		t.Fatal(err)
	}
	if origin != event.OriginInstanceID || seq != int64(event.OriginSequence) {
		t.Fatal("desk took over source origin")
	}
	if err := store.DB().QueryRow(`SELECT finish_time_ms FROM members WHERE id='member'`).Scan(&finish); err != nil || finish != event.Time {
		t.Fatalf("finish=%d %v", finish, err)
	}
	batch, err := store.PrepareObservationBatch(ctx, "100", 100, time.Now())
	if err != nil || batch != nil {
		t.Fatalf("edge leaked into desk-owned v3: %+v %v", batch, err)
	}
}

func TestEdgeLiveRejectsBindingAndDoesNotACKFailedTransaction(t *testing.T) {
	for _, kind := range []string{"event", "session", "projection fault", "journal fault"} {
		t.Run(kind, func(t *testing.T) {
			store, event := edgeLiveFixture(t)
			switch kind {
			case "event":
				event.ExternalEventID = 200
			case "session":
				event.SourceSessionID = "wrong-session"
			case "projection fault":
				if _, err := store.DB().Exec(`CREATE TRIGGER fail_edge_projection BEFORE INSERT ON results BEGIN SELECT RAISE(ABORT,'projection fault'); END`); err != nil {
					t.Fatal(err)
				}
			case "journal fault":
				if _, err := store.DB().Exec(`CREATE TRIGGER fail_edge_outbox BEFORE INSERT ON edge_observation_outbox BEGIN SELECT RAISE(ABORT,'journal fault'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if ack, err := edgeRoundTrip(t, store, event, false); ack != "" || err == nil {
				t.Fatalf("bad acceptance ACK=%q err=%v", ack, err)
			}
			if n, _ := store.CountRfidLogs(context.Background(), "100"); n != 0 {
				t.Fatalf("failed input persisted: %d", n)
			}
		})
	}
}

func edgeTestPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	_ = ln.Close()
	return port
}
func edgeDial(t *testing.T, port string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 50*time.Millisecond)
		if err == nil {
			return c
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("edge listener did not bind")
	return nil
}

func TestEdgeListenerCoexistsWithNativeAndStopJoinsConnections(t *testing.T) {
	store, event := edgeLiveFixture(t)
	manager := NewLiveManager(log.New(io.Discard, "", 0))
	defer manager.StopAll()
	edgePort := edgeTestPort(t)
	if err := manager.StartEdge(store, "100", edgePort); err != nil {
		t.Fatal(err)
	}
	conn := edgeDial(t, edgePort)
	defer conn.Close()
	status := manager.Status("100")
	if !status.AnyRunning || !status.Edge.Running || status.Running {
		t.Fatalf("edge-only state: %+v", status)
	}
	if err := manager.Start(store, "100", "0"+edgePort); err == nil {
		t.Fatal("native/edge port collision allowed")
	}
	if err := manager.ConfigureEdge(context.Background(), store, "100", nil); err == nil {
		t.Fatal("changed active source bindings")
	}
	nativePort := edgeTestPort(t)
	if nativePort == edgePort {
		t.Fatal("test reused an active port")
	}
	if err := manager.Start(store, "100", nativePort); err != nil {
		t.Fatal(err)
	}
	native := edgeDial(t, nativePort)
	defer native.Close()
	if status = manager.Status("100"); !status.Running || !status.Edge.Running {
		t.Fatal("native input disabled by edge")
	}
	nativeEPC := "E280CCDD"
	if err := store.UpsertMember(context.Background(), domain.Member{ID: "native-member", EventID: "100", RaceID: "race", EPC: &nativeEPC}); err != nil {
		t.Fatal(err)
	}
	payload, err := edge.Encode(event)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	_ = native.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write(append(payload, '\n')); err != nil {
		t.Fatal(err)
	}
	frame := fmt.Sprintf(`{"DeviceCode":"U659","time":%q,"epc":%q,"rssi":-55,"channelId":1}`, event.RTC, nativeEPC)
	if _, err := native.Write([]byte(frame)); err != nil {
		t.Fatal(err)
	}
	if ack, err := bufio.NewReader(conn).ReadString('\n'); err != nil || ack != string(edge.ACK(event)) {
		t.Fatalf("edge ACK=%q err=%v", ack, err)
	}
	if ack, err := bufio.NewReader(native).ReadString('\n'); err != nil || ack != "ok\n" {
		t.Fatalf("native ACK=%q err=%v", ack, err)
	}
	var raw, owned, relayed, results int
	if err := store.DB().QueryRow(`SELECT (SELECT COUNT(*) FROM rfid_logs),(SELECT COUNT(*) FROM observation_outbox),(SELECT COUNT(*) FROM edge_observation_outbox),(SELECT COUNT(*) FROM results)`).Scan(&raw, &owned, &relayed, &results); err != nil {
		t.Fatal(err)
	}
	if raw != 2 || owned != 1 || relayed != 1 || results != 2 {
		t.Fatalf("mixed receiver state: raw=%d owned=%d edge=%d results=%d", raw, owned, relayed, results)
	}
	manager.StopEdge("100")
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	if _, err := conn.Read(b[:]); err == nil {
		t.Fatal("old edge connection survived stop")
	}
	if status = manager.Status("100"); status.Edge.Running || !status.Running {
		t.Fatal("edge stop affected native or did not finish")
	}
	if err := manager.ConfigureEdge(context.Background(), store, "100", []domain.EdgeBinding{{Board: event.Board, SourceSessionID: "session-two"}}); err != nil {
		t.Fatal(err)
	}
	manager.Stop("100")
	if manager.Status("100").AnyRunning {
		t.Fatal("stop returned before listeners joined")
	}
	manager.StopAll()
	if err := manager.StartEdge(store, "100", edgePort); err == nil {
		t.Fatal("listener started after application shutdown")
	}
	if err := manager.Start(store, "100", nativePort); err == nil {
		t.Fatal("native listener started after shutdown")
	}
}

func TestEdgeLiveSuccessfulNativeDuplicateKeepsEdgeOrigin(t *testing.T) {
	store, event := edgeLiveFixture(t)
	if _, err := edgeRoundTrip(t, store, event, false); err != nil {
		t.Fatal(err)
	}
	stats := &LiveStats{}
	publisher := &livePublisher{store: store, eventID: "100", stats: stats}
	if err := publisher.Publish(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if stats.Duplicates.Load() != 1 {
		t.Fatal("native duplicate reinserted")
	}
	batch, err := store.PrepareObservationBatch(context.Background(), "100", 100, time.Now())
	if err != nil || batch != nil {
		t.Fatal("native repeat stole edge origin")
	}
	// The edge publisher still validates the packet even when an ID exists.
	event.ExternalEventID = 200
	p := &edgePublisher{store: store, eventID: "100", stats: &LiveStats{}}
	if err := p.Publish(context.Background(), event); err == nil || !strings.Contains(err.Error(), "another event") {
		t.Fatalf("wrong event duplicate: %v", err)
	}
}
