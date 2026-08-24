package service

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"testing"
	"time"

	"github.com/fightmaster/rfid-core/ingest"
	"github.com/fightmaster/rfid-core/tcp"
	"github.com/fightmaster/rfid-core/telemetry"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/processor"
)

// Full live chain without sockets-on-ports: a Feibot frame goes through the
// rfid-core connection handler into the SQLite publisher, lands in rfid_logs
// and derives a result immediately.
func TestLiveIngestDerivesResultImmediately(t *testing.T) {
	store := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	importFixture(t, store)

	stats := &LiveStats{}
	publisher := &livePublisher{
		store:   store,
		proc:    processor.New(sqlite.NewProcessorRepo(store), log.New(io.Discard, "", 0), false),
		eventID: "ev-100",
		stats:   stats,
	}
	pipeline := ingest.NewPipeline(publisher, 1, 16, 0)
	defer pipeline.Close()

	server, client := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		tcp.HandleConnWithContext(ctx, server, tcp.ListenerConfig{
			Name:            "test",
			Adapter:         tcp.FeibotAdapter{},
			AckMode:         tcp.AckModeOK,
			MaxInFlight:     4,
			MaxLineLenBytes: 65536,
			ReadTimeout:     2 * time.Second,
			WriteTimeout:    2 * time.Second,
		}, pipeline)
	}()

	// Finish read for mem-1 (E280AAA) at 09:20 +03 — finish checkpoint opens
	// at 09:10, no prior result → derives immediately.
	frame := `{"DeviceCode":"U659","time":"2026-06-07T09:20:00.000+03:00","epc":"E280AAA","bat":90,"rssi":-55,"channelId":1}`
	if _, err := client.Write([]byte(frame)); err != nil {
		t.Fatal(err)
	}

	// ACK "ok" confirms the publish completed.
	ack := make([]byte, 8)
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := client.Read(ack)
	if err != nil {
		t.Fatalf("ack read: %v", err)
	}
	if string(ack[:n]) != "ok\n" {
		t.Fatalf("ack = %q, want ok", ack[:n])
	}
	client.Close()
	<-done

	if got := stats.Inserted.Load(); got != 1 {
		t.Errorf("inserted = %d, want 1", got)
	}
	var outboxCount int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM observation_outbox`).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if outboxCount != 1 {
		t.Errorf("outbox rows = %d, want 1", outboxCount)
	}

	var finish *int64
	if err := store.DB().QueryRow(`SELECT finish_time_ms FROM members WHERE id='mem-1'`).Scan(&finish); err != nil {
		t.Fatal(err)
	}
	wantMs := int64(1780813200000) // 2026-06-07T09:20:00+03:00
	if finish == nil || *finish != wantMs {
		t.Fatalf("live finish = %v, want %d", finish, wantMs)
	}

	// Reader retransmit: same frame → duplicate, no error.
	server2, client2 := net.Pipe()
	go tcp.HandleConnWithContext(ctx, server2, tcp.ListenerConfig{
		Name: "test", Adapter: tcp.FeibotAdapter{}, AckMode: tcp.AckModeOK,
		MaxInFlight: 4, MaxLineLenBytes: 65536,
		ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second,
	}, pipeline)
	if _, err := client2.Write([]byte(frame)); err != nil {
		t.Fatal(err)
	}
	client2.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := client2.Read(ack); err != nil {
		t.Fatalf("retransmit ack: %v", err)
	}
	client2.Close()
	if got := stats.Duplicates.Load(); got != 1 {
		t.Errorf("duplicates = %d, want 1", got)
	}
}

func TestManualFinishOverridesAndSurvivesRecount(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	importFixture(t, store)
	rec := NewRecounter(store, log.New(io.Discard, "", 0), false)

	// Chip finish from the fixture log (09:10): baseline.
	if _, err := rec.Recount(ctx, "ev-100", ""); err != nil {
		t.Fatal(err)
	}

	// Judge enters 09:30 manually — overrides the chip time.
	manualMs := int64(1780813800000)
	if _, err := ManualFinish(ctx, store, "ev-100", "mem-1", manualMs); err != nil {
		t.Fatal(err)
	}
	assertFinish(t, store, "mem-1", &manualMs)

	// Recount replays chip data, then re-applies the manual entry on top.
	if _, err := rec.Recount(ctx, "ev-100", ""); err != nil {
		t.Fatal(err)
	}
	assertFinish(t, store, "mem-1", &manualMs)

	// Judge deletes the entry → next recount restores the chip time.
	passes, err := LoadMemberPasses(ctx, store, "mem-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(passes.ManualResults) != 1 {
		t.Fatalf("manual results = %+v, want 1", passes.ManualResults)
	}
	if _, err := DeleteManualResult(ctx, store, "ev-100", passes.ManualResults[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := rec.Recount(ctx, "ev-100", ""); err != nil {
		t.Fatal(err)
	}
	chipMs := int64(1780812600000) // fixture log-active at 09:10
	assertFinish(t, store, "mem-1", &chipMs)
}

func assertFinish(t *testing.T, store *sqlite.Store, memberID string, want *int64) {
	t.Helper()
	var finish *int64
	if err := store.DB().QueryRow(`SELECT finish_time_ms FROM members WHERE id = ?`, memberID).Scan(&finish); err != nil {
		t.Fatal(err)
	}
	switch {
	case want == nil && finish != nil:
		t.Fatalf("finish = %d, want nil", *finish)
	case want != nil && (finish == nil || *finish != *want):
		t.Fatalf("finish = %v, want %d", fmtPtr(finish), *want)
	}
}

var _ = fmt.Sprintf

// Feibot heartbeats flow into the reader-monitoring panel: battery, tag
// counters and freshness per device.
func TestLiveHeartbeatFeedsReaderStatus(t *testing.T) {
	store := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	importFixture(t, store)

	manager := NewLiveManager(log.New(io.Discard, "", 0))
	stats := &LiveStats{}
	metrics := telemetry.NewRegistry()
	session := &liveSession{port: "test", stats: stats, metrics: metrics}
	manager.sessions["ev-100"] = session

	publisher := &livePublisher{
		store:   store,
		proc:    processor.New(sqlite.NewProcessorRepo(store), log.New(io.Discard, "", 0), false),
		eventID: "ev-100",
		stats:   stats,
	}
	pipeline := ingest.NewPipeline(publisher, 1, 16, 0)
	defer pipeline.Close()

	server, client := net.Pipe()
	go tcp.HandleConnWithContext(ctx, server, tcp.ListenerConfig{
		Name: "test", Adapter: tcp.FeibotAdapter{}, AckMode: tcp.AckModeOK,
		MaxInFlight: 4, MaxLineLenBytes: 65536,
		ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second,
		Metrics: metrics,
	}, pipeline)

	hb := `[{"DeviceCode":"U659","DeviceType":"Feibot","batteryPercent":78,"totalTagsRead":1240,"differentTagsRead":182,"Timestamp":"2026-06-07T09:25:00.000+03:00","reader1Working":"1","reader1Power":30}]`
	if _, err := client.Write([]byte(hb)); err != nil {
		t.Fatal(err)
	}
	ack := make([]byte, 8)
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := client.Read(ack); err != nil {
		t.Fatalf("heartbeat ack: %v", err)
	}
	client.Close()

	status := manager.Status("ev-100")
	if len(status.Readers) != 1 {
		t.Fatalf("readers = %+v, want 1", status.Readers)
	}
	r := status.Readers[0]
	if r.Device != "U659" || r.BatteryPercent != 78 || r.TotalTagsRead != 1240 {
		t.Errorf("reader = %+v", r)
	}
	if r.Heartbeats != 1 {
		t.Errorf("heartbeats = %d, want 1", r.Heartbeats)
	}
}
