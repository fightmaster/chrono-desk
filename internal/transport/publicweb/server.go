// Package publicweb serves a read-only results page over the venue LAN. At a
// competition without internet the only copy of the live results is on the
// desk; people who sign certificates, engrave medal times or run social media
// need to read them from their own phones. This is a SEPARATE server from
// internal/transport/httpapi: it exposes only GET endpoints with a PII-trimmed
// projection (no date of birth, no member ids), never any mutation, and the
// LAN listener is opened on 0.0.0.0 ONLY while the operator has switched the
// broadcast on — nothing is reachable from the network by default.
package publicweb

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/service"
)

//go:embed public.html
var indexHTML []byte

// DefaultPort is the fixed LAN port (override with CHRONO_PUBLIC_PORT) so the
// shared address stays stable for the QR code and word of mouth.
const DefaultPort = 8090

type Server struct {
	events *service.EventService
	logger *log.Logger
	port   int

	mu          sync.Mutex
	publishedID string       // the one event currently broadcast ("" = off)
	httpServer  *http.Server // non-nil only while the LAN listener is up
}

// New builds the server without opening any port. Call Publish to start
// broadcasting. port==0 selects DefaultPort.
func New(events *service.EventService, logger *log.Logger, port int) *Server {
	if port == 0 {
		port = DefaultPort
	}
	return &Server{events: events, logger: logger, port: port}
}

// Publish broadcasts eventID, opening the LAN listener on first use. Calling it
// again only switches which event is served (idempotent on the listener).
func (s *Server) Publish(eventID string) error {
	if eventID == "" {
		return fmt.Errorf("укажите событие для трансляции")
	}
	// Fail early if the event file can't be opened.
	if _, err := s.events.Open(eventID); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.publishedID = eventID
	if s.httpServer != nil {
		return nil // already serving
	}

	addr := fmt.Sprintf("0.0.0.0:%d", s.port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		s.publishedID = ""
		return fmt.Errorf("не удалось открыть порт %d: %w", s.port, err)
	}
	srv := &http.Server{Handler: s.routes(), ReadHeaderTimeout: 5 * time.Second}
	s.httpServer = srv
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Printf("public web stopped: %v", err)
		}
	}()
	s.logger.Printf("public results broadcast on :%d for event %s", s.port, eventID)
	return nil
}

// Unpublish stops the broadcast and closes the LAN listener.
func (s *Server) Unpublish() {
	s.mu.Lock()
	srv := s.httpServer
	s.httpServer = nil
	s.publishedID = ""
	s.mu.Unlock()
	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// Shutdown closes the listener (if any) on application exit.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	srv := s.httpServer
	s.httpServer = nil
	s.publishedID = ""
	s.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

// Status reports whether the broadcast is live, which event, and the LAN URLs
// people can open. The QR code is rendered by the localhost control API from
// PrimaryURL.
type Status struct {
	Running bool     `json:"running"`
	EventID string   `json:"event_id"`
	Port    int      `json:"port"`
	URLs    []string `json:"urls"`
}

func (s *Server) Status() Status {
	s.mu.Lock()
	st := Status{Running: s.httpServer != nil, EventID: s.publishedID, Port: s.port}
	s.mu.Unlock()
	if st.Running {
		st.URLs = lanURLs(s.port)
	}
	return st
}

func (s *Server) published() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.publishedID
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	// GET-only patterns: ServeMux answers any other method with 405, so the
	// network can never reach a write path here.
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/meta", s.handleMeta)
	mux.HandleFunc("GET /api/races", s.handleRaces)
	mux.HandleFunc("GET /api/races/{raceID}", s.handleProtocol)
	return noStore(mux)
}

func (s *Server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	eventID := s.published()
	if eventID == "" {
		writeJSON(w, http.StatusOK, map[string]any{"published": false})
		return
	}
	store, err := s.events.Open(eventID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"published": false})
		return
	}
	ev, err := service.FirstEvent(r.Context(), store)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"published": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"published": true, "event_name": ev.Name, "event_date": ev.Date,
	})
}

type raceJSON struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Format string `json:"format"`
}

func (s *Server) handleRaces(w http.ResponseWriter, r *http.Request) {
	eventID := s.published()
	if eventID == "" {
		writeJSON(w, http.StatusOK, map[string]any{"published": false, "races": []raceJSON{}})
		return
	}
	store, err := s.events.Open(eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	races, err := store.ListRaces(r.Context(), eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]raceJSON, 0, len(races))
	for _, rc := range races {
		out = append(out, raceJSON{ID: rc.ID, Name: rc.Name, Format: string(rc.Format)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"published": true, "races": out})
}

func (s *Server) handleProtocol(w http.ResponseWriter, r *http.Request) {
	eventID := s.published()
	if eventID == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"published": false})
		return
	}
	store, err := s.events.Open(eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	full, err := service.BuildProtocol(r.Context(), store, r.PathValue("raceID"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPublicProtocol(full))
}

// ─── Public projection (PII-trimmed) ──────────────────────────────────────────

// publicRow is a protocol line stripped of personal data: NO date of birth and
// NO internal member/category ids — only what a spectator board needs.
type publicRow struct {
	Number        *int64  `json:"number"`
	FirstName     string  `json:"first_name"`
	LastName      string  `json:"last_name"`
	Gender        *string `json:"gender"`
	Team          *string `json:"team"`
	City          *string `json:"city"`
	CategoryName  *string `json:"category_name"`
	Status        string  `json:"status"`
	Place         *int    `json:"place"`
	GenderPlace   *int    `json:"gender_place"`
	CategoryPlace *int    `json:"category_place"`
	CleanTime     *string `json:"clean_time"`
	ElapsedMs     *int64  `json:"elapsed_ms,omitempty"`
}

type publicProtocol struct {
	RaceID   string                 `json:"race_id"`
	RaceName string                 `json:"race_name"`
	Format   string                 `json:"format"`
	Counts   service.ProtocolCounts `json:"counts"`
	Rows     []publicRow            `json:"rows"`
}

func toPublicProtocol(p service.ProtocolResponse) publicProtocol {
	rows := make([]publicRow, 0, len(p.Rows))
	for _, r := range p.Rows {
		rows = append(rows, publicRow{
			Number: r.Number, FirstName: r.FirstName, LastName: r.LastName,
			Gender: r.Gender, Team: r.Team, City: r.City,
			CategoryName: r.CategoryName, Status: r.Status,
			Place: r.Place, GenderPlace: r.GenderPlace, CategoryPlace: r.CategoryPlace,
			CleanTime: r.CleanTime, ElapsedMs: r.ElapsedMs,
		})
	}
	return publicProtocol{
		RaceID: p.RaceID, RaceName: p.RaceName, Format: p.Format,
		Counts: p.Counts, Rows: rows,
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// lanURLs lists http://<ip>:<port> for every real, reachable IPv4 interface.
// Virtual interfaces (Docker bridges, VMs, VPN tunnels) are skipped — a phone on
// the venue Wi-Fi can never reach e.g. docker0's 172.17.x.x, and a QR pointing
// there is a dead link. A venue machine may still have both Ethernet and Wi-Fi
// up, so several candidates can remain; the caller offers a QR for each.
func lanURLs(port int) []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var urls []string
	for _, iface := range ifaces {
		// Up, not loopback, not a point-to-point tunnel (VPNs).
		if iface.Flags&net.FlagUp == 0 ||
			iface.Flags&net.FlagLoopback != 0 ||
			iface.Flags&net.FlagPointToPoint != 0 {
			continue
		}
		if isVirtualIface(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue // IPv4 only, no loopback / 169.254.x.x
			}
			urls = append(urls, fmt.Sprintf("http://%s:%d", ip4.String(), port))
		}
	}
	return urls
}

// isVirtualIface reports whether an interface name belongs to Docker, a VM, a
// container veth, or a VPN — addresses unreachable from the venue Wi-Fi.
func isVirtualIface(name string) bool {
	name = strings.ToLower(name)
	for _, p := range []string{
		"docker", "br-", "veth", "virbr", "vmnet", "vboxnet",
		"tap", "tun", "tailscale", "wg", "zt", "utun", "awdl", "llw",
	} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	s.logger.Printf("public web error: %v", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		_ = err
	}
}
