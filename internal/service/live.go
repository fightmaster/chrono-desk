package service

import (
	"context"
	"fmt"
	"log"
	"net"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"gitlab.com/fightmaster1/rfid-core/edge"
	"gitlab.com/fightmaster1/rfid-core/ingest"
	"gitlab.com/fightmaster1/rfid-core/tcp"
	"gitlab.com/fightmaster1/rfid-core/telemetry"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/processor"
)

// Native Feibot and owned edge transports use separate opt-in listeners on the
// venue LAN. Both reuse core framing; source provenance differs by protocol.

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
	AnyRunning bool           `json:"any_running"`
	Edge       EdgeLiveStatus `json:"edge"`
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
	done    chan struct{}

	mu       sync.Mutex
	lastErr  string
	finished bool
	combined bool
}

// LiveManager runs at most one listener per event and transport profile.
type LiveManager struct {
	logger *log.Logger

	mu           sync.Mutex
	sessions     map[string]*liveSession
	edgeSessions map[string]*liveSession
	closed       bool
}

func NewLiveManager(logger *log.Logger) *LiveManager {
	return &LiveManager{logger: logger, sessions: map[string]*liveSession{}, edgeSessions: map[string]*liveSession{}}
}

// Start launches the event's normal reader input on 0.0.0.0:port. Events with
// explicit Edge source bindings use the shared Feibot/Edge adapter, so the
// operator still has one start button and one address. Events without bindings
// preserve the legacy Feibot-only listener.
func (m *LiveManager) Start(store *sqlite.Store, eventID, port string) error {
	m.mu.Lock()
	edgeSession := m.edgeSessions[eventID]
	edgeAlreadyRunning := edgeSession != nil && !edgeSession.isFinished()
	m.mu.Unlock()
	// Preserve the advanced legacy layout where an explicit Edge-only input
	// already occupies its own port and native Feibot is started beside it.
	if edgeAlreadyRunning {
		return m.startListener(store, eventID, port, false, false)
	}
	bindings, err := store.EdgeBindings(context.Background(), eventID)
	if err != nil {
		return err
	}
	if len(bindings) > 0 {
		return m.startListener(store, eventID, port, true, true)
	}
	return m.startListener(store, eventID, port, false, false)
}

func (m *LiveManager) startListener(store *sqlite.Store, eventID, port string, edgeMode, combined bool) error {
	if port == "" {
		port = "5084"
		if edgeMode && !combined {
			port = "5085"
		}
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return fmt.Errorf("некорректный TCP-порт")
	}
	port = strconv.Itoa(number)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return fmt.Errorf("приёмники приложения уже остановлены")
	}
	selected := m.sessions
	if edgeMode {
		selected = m.edgeSessions
		id, err := strconv.ParseInt(eventID, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != eventID {
			return fmt.Errorf("edge требует числовой идентификатор события RUN5/Chrono")
		}
		bindings, err := store.EdgeBindings(context.Background(), eventID)
		if err != nil {
			return err
		}
		if len(bindings) == 0 {
			return fmt.Errorf("сначала задайте board и сессию sidecar для события")
		}
	}
	if s, ok := selected[eventID]; ok && !s.isFinished() {
		return fmt.Errorf("приём для события уже запущен на порту %s", s.port)
	}
	for _, sessions := range []map[string]*liveSession{m.sessions, m.edgeSessions} {
		for id, s := range sessions {
			if !s.isFinished() && s.port == port {
				return fmt.Errorf("порт %s уже занят событием %s", port, id)
			}
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	session := &liveSession{
		port: port, cancel: cancel,
		combined: combined,
		stats:    &LiveStats{},
		metrics:  telemetry.NewRegistry(),
		done:     make(chan struct{}),
	}
	selected[eventID] = session

	var publisher ingest.Publisher = &livePublisher{
		store:   store,
		proc:    processor.New(sqlite.NewProcessorRepo(store), m.logger, false),
		eventID: eventID,
		stats:   session.stats,
	}
	cfg := tcp.ListenerConfig{Name: "chrono-desk:" + eventID, Host: "", Port: port, Adapter: tcp.FeibotAdapter{}, AckMode: tcp.AckModeOK, MaxInFlight: 64, Metrics: session.metrics}
	if edgeMode {
		cfg.Name = "chrono-desk-edge:" + eventID
		cfg.Adapter = tcp.EdgeAdapter{}
		cfg.AckMode = tcp.AckModeID
		cfg.MaxLineLenBytes = edge.MaxFrameBytes
		cfg.MaxConnections = 16
		cfg.ReadTimeout = 30 * time.Second
		cfg.WriteTimeout = 5 * time.Second
		publisher = &edgePublisher{store: store, eventID: eventID, stats: session.stats, logger: m.logger, onError: session.recordError}
		if combined {
			cfg.Name = "chrono-desk-feibot-edge:" + eventID
			cfg.Adapter = tcp.FeibotEdgeAdapter{}
			// Native arrays retain their bounded 64KiB framing allowance;
			// the shared Edge codec still enforces 10KiB for an owned object.
			cfg.MaxLineLenBytes = 65536
			publisher = combinedPublisher{
				native: &livePublisher{store: store, proc: processor.New(sqlite.NewProcessorRepo(store), m.logger, false), eventID: eventID, stats: session.stats},
				edge:   publisher,
			}
		}
	}
	pipeline := ingest.NewPipeline(publisher, 1, 256, 0)

	go func() {
		defer close(session.done)
		err := tcp.ServeListener(ctx, cfg, pipeline)
		pipeline.Close()
		session.finish(err)
		if err != nil {
			m.logger.Printf("live listener %s stopped: %v", eventID, err)
		}
	}()
	return nil
}

func (m *LiveManager) Stop(eventID string) {
	m.mu.Lock()
	sessions := []*liveSession{m.sessions[eventID], m.edgeSessions[eventID]}
	m.mu.Unlock()
	stopLiveSessions(sessions)
}

func (m *LiveManager) Status(eventID string) LiveStatus {
	m.mu.Lock()
	s, ok := m.sessions[eventID]
	edgeSession := m.edgeSessions[eventID]
	m.mu.Unlock()

	status := LiveStatus{IPs: lanIPs(), Edge: edgeSessionStatus(edgeSession)}
	status.Readers = readerStatuses(s, edgeSession)
	status.AnyRunning = status.Edge.Running
	if !ok {
		return status
	}
	status.Running = !s.isFinished()
	status.AnyRunning = status.AnyRunning || status.Running
	status.Port = s.port
	status.Received = s.stats.Received.Load()
	status.Inserted = s.stats.Inserted.Load()
	status.Duplicates = s.stats.Duplicates.Load()
	status.Errors = s.stats.Errors.Load()
	status.LastReadMs = s.stats.LastReadMs.Load()
	status.LastError = s.lastError()

	return status
}

func readerStatuses(sessions ...*liveSession) []ReaderStatus {
	now := time.Now().Unix()
	byDevice := map[string]ReaderStatus{}
	for _, session := range sessions {
		if session == nil || session.metrics == nil {
			continue
		}
		for _, hb := range session.metrics.FeibotSnapshots() {
			if previous, ok := byDevice[hb.DeviceCode]; ok && previous.LastSeenUnix > hb.LastHeartbeatUnix {
				continue
			}
			byDevice[hb.DeviceCode] = ReaderStatus{
				Device:            hb.DeviceCode,
				BatteryPercent:    hb.BatteryPercent,
				TotalTagsRead:     hb.TotalTagsRead,
				DifferentTagsRead: hb.DifferentTagsRead,
				Heartbeats:        hb.HeartbeatTotal,
				LastSeenUnix:      hb.LastHeartbeatUnix,
				AgeSeconds:        max(0, now-hb.LastHeartbeatUnix),
			}
		}
	}
	readers := make([]ReaderStatus, 0, len(byDevice))
	for _, reader := range byDevice {
		readers = append(readers, reader)
	}
	sort.Slice(readers, func(i, j int) bool {
		return readers[i].Device < readers[j].Device
	})
	return readers
}

// StopAll shuts every listener down (app exit).
func (m *LiveManager) StopAll() {
	m.mu.Lock()
	m.closed = true
	var sessions []*liveSession
	for _, group := range []map[string]*liveSession{m.sessions, m.edgeSessions} {
		for _, s := range group {
			sessions = append(sessions, s)
		}
	}
	m.mu.Unlock()
	stopLiveSessions(sessions)
}

func stopLiveSessions(sessions []*liveSession) {
	for _, s := range sessions {
		if s != nil && s.cancel != nil {
			s.cancel()
		}
	}
	for _, s := range sessions {
		if s != nil && s.done != nil {
			<-s.done
		}
	}
}

func (s *liveSession) recordError(err error) { s.mu.Lock(); s.lastErr = err.Error(); s.mu.Unlock() }

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
