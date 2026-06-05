// Package httpapi exposes the application core over HTTP. The Wails frontend
// is its first consumer; later the same API serves the local network (judges'
// tablets) and a headless mode.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/service"
)

type Server struct {
	httpServer *http.Server
	listener   net.Listener
	events     *service.EventManager
	logger     *log.Logger
}

// New binds addr (use "127.0.0.1:0" for an ephemeral local port) and builds
// the route table. Call Start to begin serving.
func New(addr string, events *service.EventManager, logger *log.Logger) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}

	s := &Server{
		listener: ln,
		events:   events,
		logger:   logger,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /api/events", s.handleListEvents)
	mux.HandleFunc("POST /api/events/import", s.handleImportEvent)
	mux.HandleFunc("POST /api/events/{id}/rfid-import", s.handleRfidImport)
	mux.HandleFunc("POST /api/events/{id}/recount", s.handleRecount)
	mux.HandleFunc("GET /api/events/{id}/races", s.handleListRaces)
	mux.HandleFunc("GET /api/events/{id}/races/{raceID}/protocol", s.handleProtocol)
	mux.HandleFunc("GET /api/events/{id}/races/{raceID}/protocol.xlsx", s.handleProtocolXLSX)
	mux.HandleFunc("POST /api/events/{id}/races/{raceID}/export-xlsx", s.handleExportXLSX)
	mux.HandleFunc("POST /api/events/{id}/edits", s.handleApplyEdit)
	mux.HandleFunc("GET /api/events/{id}/edits", s.handleListEdits)
	mux.HandleFunc("GET /api/events/{id}/checkpoints", s.handleListCheckpoints)
	mux.HandleFunc("GET /api/events/{id}/members/{memberID}/passes", s.handleMemberPasses)

	s.httpServer = &http.Server{
		// The Wails webview loads the UI from its own origin, so the
		// localhost API must answer CORS preflight.
		Handler:           cors(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s, nil
}

// BaseURL returns the address the frontend should talk to.
func (s *Server) BaseURL() string {
	return "http://" + s.listener.Addr().String()
}

// Start serves until Shutdown; it returns http.ErrServerClosed on clean stop.
func (s *Server) Start() error {
	return s.httpServer.Serve(s.listener)
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	infos, err := s.events.List(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if infos == nil {
		infos = []service.EventInfo{}
	}
	writeJSON(w, http.StatusOK, infos)
}

func (s *Server) handleImportEvent(w http.ResponseWriter, r *http.Request) {
	stats, err := s.events.ImportExport(r.Context(), http.MaxBytesReader(w, r.Body, 512<<20))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleRfidImport(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("id")
	device := r.URL.Query().Get("device")
	tz := r.URL.Query().Get("tz")
	if tz == "" {
		tz = "UTC"
	}

	store, err := s.events.Open(eventID)
	if err != nil {
		s.fail(w, err)
		return
	}

	res, err := service.NewFeibotCsvImporter(store).
		Import(r.Context(), http.MaxBytesReader(w, r.Body, 512<<20), eventID, device, tz)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleRecount(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("id")
	raceID := r.URL.Query().Get("race")

	store, err := s.events.Open(eventID)
	if err != nil {
		s.fail(w, err)
		return
	}

	stats, err := service.NewRecounter(store, s.logger, false).Recount(r.Context(), eventID, raceID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleListRaces(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	races, err := store.ListRaces(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}

	type raceJSON struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Date        string `json:"date"`
		Format      string `json:"format"`
		StartedAtMs *int64 `json:"started_at_ms"`
	}
	out := make([]raceJSON, 0, len(races))
	for _, rc := range races {
		out = append(out, raceJSON{
			ID: rc.ID, Name: rc.Name, Date: rc.Date, Format: string(rc.Format),
			StartedAtMs: rc.StartedAtMs,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleProtocol(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}

	protocol, err := service.BuildProtocol(r.Context(), store, r.PathValue("raceID"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, protocol)
}

// handleProtocolXLSX streams the workbook — handy from a regular browser on
// the LAN.
func (s *Server) handleProtocolXLSX(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	data, name, err := service.BuildProtocolXLSX(r.Context(), store, r.PathValue("raceID"))
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	_, _ = w.Write(data)
}

// handleExportXLSX saves the workbook into the user's Downloads directory —
// the desktop-friendly path (webviews are unreliable at file downloads).
func (s *Server) handleExportXLSX(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	data, name, err := service.BuildProtocolXLSX(r.Context(), store, r.PathValue("raceID"))
	if err != nil {
		s.fail(w, err)
		return
	}
	path, err := service.SaveToDownloads(name, data)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": path})
}

func (s *Server) handleApplyEdit(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	var req service.EditRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		s.fail(w, err)
		return
	}
	res, err := service.ApplyEdit(r.Context(), store, req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleListEdits(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	changes, err := store.ListLocalChanges(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if changes == nil {
		changes = []sqlite.LocalChange{}
	}
	writeJSON(w, http.StatusOK, changes)
}

func (s *Server) handleListCheckpoints(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	checkpoints, err := store.ListCheckpointsByEvent(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, checkpoints)
}

func (s *Server) handleMemberPasses(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	passes, err := service.LoadMemberPasses(r.Context(), store, r.PathValue("memberID"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, passes)
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	s.logger.Printf("http error: %v", err)
	status := http.StatusInternalServerError
	var maxBytes *http.MaxBytesError
	if errors.As(err, &maxBytes) {
		status = http.StatusRequestEntityTooLarge
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		_ = err
	}
}
