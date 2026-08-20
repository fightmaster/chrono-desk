package service

import (
	"context"
	"fmt"
	"log"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"gitlab.com/fightmaster1/rfid-core/ingest"
	"gitlab.com/fightmaster1/rfid-core/tcp"
	"gitlab.com/fightmaster1/rfid-core/telemetry"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/processor"
)

// Live TCP ingest: the desktop acts as a Feibot "server" on the venue LAN
// (the reader supports two upload targets — the site stays primary, this app
// is the second). Reads land in the event database and run through the same
// derivation engine immediately, so the finish judge sees results live.

// LiveStats are monotonic counters for the status panel.
type LiveStats struct {
	Received   atomic.Int64
	Inserted   atomic.Int64
	Duplicates atomic.Int64
	Errors     atomic.Int64
	LastReadMs atomic.Int64
}

// ReaderStatus is one Feibot device as seen through its heartbeats — the
// "is the reader alive and charged" panel for the start crew.
type ReaderStatus struct {
	Device            string `json:"device"`
	BatteryPercent    int64  `json:"battery_percent"`
	TotalTagsRead     int64  `json:"total_tags_read"`
	DifferentTagsRead int64  `json:"different_tags_read"`
	Heartbeats        int64  `json:"heartbeats"`
	LastSeenUnix      int64  `json:"last_seen_unix"`
	AgeSeconds        int64  `json:"age_seconds"`
}

// LiveStatus is the JSON snapshot for the UI.
type LiveStatus struct {
	Running    bool           `json:"running"`
	Port       string         `json:"port"`
	IPs        []string       `json:"ips"`
	Received   int64          `json:"received"`
	Inserted   int64          `json:"inserted"`
	Duplicates int64          `json:"duplicates"`
	Errors     int64          `json:"errors"`
	LastReadMs int64          `json:"last_read_ms"`
	LastError  string         `json:"last_error"`
	Readers    []ReaderStatus `json:"readers"`
}

type liveSession struct {
	port    string
	cancel  context.CancelFunc
	stats   *LiveStats
	metrics *telemetry.Registry

	mu       sync.Mutex
	lastErr  string
	finished bool
}

// LiveManager runs at most one listener per event.
type LiveManager struct {
	logger *log.Logger

	mu       sync.Mutex
	sessions map[string]*liveSession
}

func NewLiveManager(logger *log.Logger) *LiveManager {
	return &LiveManager{logger: logger, sessions: map[string]*liveSession{}}
}

// Start launches a Feibot TCP listener for the event on 0.0.0.0:port.
func (m *LiveManager) Start(store *sqlite.Store, eventID, port string) error {
	if port == "" {
		port = "5084"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[eventID]; ok && !s.isFinished() {
		return fmt.Errorf("приём для события уже запущен на порту %s", s.port)
	}
	for id, s := range m.sessions {
		if !s.isFinished() && s.port == port {
			return fmt.Errorf("порт %s уже занят событием %s", port, id)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	session := &liveSession{
		port: port, cancel: cancel,
		stats:   &LiveStats{},
		metrics: telemetry.NewRegistry(),
	}
	m.sessions[eventID] = session

	publisher := &livePublisher{
		store:   store,
		proc:    processor.New(sqlite.NewProcessorRepo(store), m.logger, false),
		eventID: eventID,
		stats:   session.stats,
	}
	pipeline := ingest.NewPipeline(publisher, 1, 256, 0)

	go func() {
		defer pipeline.Close()
		err := tcp.ServeListener(ctx, tcp.ListenerConfig{
			Name:        "chrono-desk:" + eventID,
			Host:        "", // all interfaces — the reader connects over the LAN
			Port:        port,
			Adapter:     tcp.FeibotAdapter{},
			AckMode:     tcp.AckModeOK,
			MaxInFlight: 64,
			Metrics:     session.metrics, // Feibot heartbeats → reader monitoring
		}, pipeline)
		session.finish(err)
		if err != nil {
			m.logger.Printf("live listener %s stopped: %v", eventID, err)
		}
	}()
	return nil
}

func (m *LiveManager) Stop(eventID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[eventID]; ok {
		s.cancel()
	}
}

func (m *LiveManager) Status(eventID string) LiveStatus {
	m.mu.Lock()
	s, ok := m.sessions[eventID]
	m.mu.Unlock()

	status := LiveStatus{IPs: lanIPs()}
	if !ok {
		return status
	}
	status.Running = !s.isFinished()
	status.Port = s.port
	status.Received = s.stats.Received.Load()
	status.Inserted = s.stats.Inserted.Load()
	status.Duplicates = s.stats.Duplicates.Load()
	status.Errors = s.stats.Errors.Load()
	status.LastReadMs = s.stats.LastReadMs.Load()
	status.LastError = s.lastError()

	now := time.Now().Unix()
	for _, hb := range s.metrics.FeibotSnapshots() {
		status.Readers = append(status.Readers, ReaderStatus{
			Device:            hb.DeviceCode,
			BatteryPercent:    hb.BatteryPercent,
			TotalTagsRead:     hb.TotalTagsRead,
			DifferentTagsRead: hb.DifferentTagsRead,
			Heartbeats:        hb.HeartbeatTotal,
			LastSeenUnix:      hb.LastHeartbeatUnix,
			AgeSeconds:        max(0, now-hb.LastHeartbeatUnix),
		})
	}
	sort.Slice(status.Readers, func(i, j int) bool {
		return status.Readers[i].Device < status.Readers[j].Device
	})
	return status
}

// StopAll shuts every listener down (app exit).
func (m *LiveManager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		s.cancel()
	}
}

func (s *liveSession) finish(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished = true
	if err != nil {
		s.lastErr = err.Error()
	}
}

func (s *liveSession) isFinished() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finished
}

func (s *liveSession) lastError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

// livePublisher lands one parsed read into the event database and derives its
// result immediately — the SQLite counterpart of rfid-hub's Redis sink plus
// rfid-sync's live processing, in-process.
type livePublisher struct {
	store   *sqlite.Store
	proc    *processor.Processor
	eventID string
	stats   *LiveStats
}

func (p *livePublisher) Publish(ctx context.Context, ev ingest.Event) error {
	p.stats.Received.Add(1)
	p.stats.LastReadMs.Store(ev.Time)

	logEntry := domain.RfidLog{
		ID:              ev.ID,
		EventID:         p.eventID,
		Status:          ev.Status,
		Number:          ev.Number,
		TimeMs:          ev.Time,
		Ant:             ev.Ant,
		EPC:             ev.EPC,
		RSSI:            ev.RSSI,
		Board:           ev.Board,
		CaptureSourceID: "chrono-desk:" + p.eventID + ":" + ev.Board,
	}
	inserted, err := p.store.InsertOwnedRfidLogs(ctx, []domain.RfidLog{logEntry})
	if err != nil {
		p.stats.Errors.Add(1)
		return err
	}
	if inserted == 0 {
		p.stats.Duplicates.Add(1)
		return nil // reader retransmit — already processed
	}
	p.stats.Inserted.Add(1)

	if err := p.proc.Process(ctx, logEntry, ""); err != nil {
		p.stats.Errors.Add(1)
		return err
	}
	return nil
}

// lanIPs lists the machine's IPv4 addresses for the "point the reader here"
// hint, private ranges first.
func lanIPs() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var private, other []string
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipNet.IP.To4()
		if ip == nil || ip.IsLoopback() {
			continue
		}
		if ip.IsPrivate() {
			private = append(private, ip.String())
		} else {
			other = append(other, ip.String())
		}
	}
	return append(private, other...)
}
