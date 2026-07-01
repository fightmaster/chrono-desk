// Package httpapi exposes the application core over HTTP. The Wails frontend
// is its first consumer; later the same API serves the local network (judges'
// tablets) and a headless mode.
package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	qrcode "github.com/skip2/go-qrcode"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/service"
	"gitlab.com/fightmaster1/chrono-desk/internal/transport/publicweb"
	"gitlab.com/fightmaster1/chrono-desk/internal/version"
)

type Server struct {
	httpServer *http.Server
	listener   net.Listener
	events     *service.EventManager
	live       *service.LiveManager
	photos     *service.PhotoManager
	photoCache *service.PhotoCache
	public     *publicweb.Server
	logger     *log.Logger
}

// New binds addr (use "127.0.0.1:0" for an ephemeral local port) and builds
// the route table. Call Start to begin serving. public is the read-only LAN
// results broadcaster controlled by the /api/public/* endpoints.
func New(addr string, events *service.EventManager, public *publicweb.Server, logger *log.Logger) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}

	s := &Server{
		listener: ln,
		events:   events,
		live:     service.NewLiveManager(logger),
		photos:   service.NewPhotoManager(logger),
		public:   public,
		logger:   logger,
	}

	// Local write-once cache for finish-photo JPEGs (served via the image proxy).
	cacheRoot, err := os.UserCacheDir()
	if err != nil || cacheRoot == "" {
		cacheRoot = os.TempDir()
	}
	if pc, perr := service.NewPhotoCache(filepath.Join(cacheRoot, "chrono-desk", "photos")); perr != nil {
		logger.Printf("photo cache disabled: %v", perr)
	} else {
		s.photoCache = pc
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /api/version", s.handleVersion)
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
	mux.HandleFunc("POST /api/events/{id}/members", s.handleCreateMember)
	mux.HandleFunc("GET /api/events/{id}/members/{memberID}", s.handleGetMember)
	mux.HandleFunc("GET /api/events/{id}/categories", s.handleListCategories)
	mux.HandleFunc("GET /api/events/{id}/races/{raceID}/categories", s.handleListRaceCategories)
	mux.HandleFunc("POST /api/events/{id}/races/{raceID}/categories", s.handleAttachCategory)
	mux.HandleFunc("DELETE /api/events/{id}/races/{raceID}/categories/{categoryID}", s.handleDetachCategory)
	mux.HandleFunc("GET /api/events/{id}/members", s.handleListMembers)
	mux.HandleFunc("POST /api/events/{id}/checkpoints", s.handleCreateCheckpoint)
	mux.HandleFunc("DELETE /api/events/{id}/checkpoints/{cpID}", s.handleDeleteCheckpoint)
	mux.HandleFunc("POST /api/events/{id}/backup", s.handleBackup)
	mux.HandleFunc("POST /api/events/{id}/export-json", s.handleExportJSON)
	mux.HandleFunc("POST /api/events/{id}/live/start", s.handleLiveStart)
	mux.HandleFunc("POST /api/events/{id}/live/stop", s.handleLiveStop)
	mux.HandleFunc("GET /api/events/{id}/live/status", s.handleLiveStatus)
	mux.HandleFunc("GET /api/events/{id}/live/feed", s.handleLiveFeed)
	mux.HandleFunc("POST /api/events/{id}/members/{memberID}/manual-finish", s.handleManualFinish)
	mux.HandleFunc("GET /api/events/{id}/manual-results", s.handleListManualResults)
	mux.HandleFunc("DELETE /api/events/{id}/results/{resultID}", s.handleDeleteManualResult)
	mux.HandleFunc("POST /api/events/{id}/captures", s.handleCreateCapture)
	mux.HandleFunc("GET /api/events/{id}/captures", s.handleListCaptures)
	mux.HandleFunc("DELETE /api/events/{id}/captures/{capID}", s.handleDeleteCapture)
	mux.HandleFunc("GET /api/events/{id}/photos/sources", s.handleListPhotoSources)
	mux.HandleFunc("POST /api/events/{id}/photos/sources", s.handleAddPhotoSource)
	mux.HandleFunc("DELETE /api/events/{id}/photos/sources", s.handleDeletePhotoSource)
	mux.HandleFunc("POST /api/events/{id}/photos/poll", s.handlePollPhotos)
	mux.HandleFunc("GET /api/events/{id}/photos/status", s.handlePhotoStatus)
	mux.HandleFunc("GET /api/events/{id}/photos/recent", s.handleRecentPhotos)
	mux.HandleFunc("GET /api/events/{id}/photos/merged", s.handleMergedPhotos)
	mux.HandleFunc("POST /api/events/{id}/photos/export-csv", s.handleExportFinishesCSV)
	mux.HandleFunc("GET /api/events/{id}/photos/img", s.handlePhotoImage)
	mux.HandleFunc("GET /api/events/{id}/photos", s.handleMatchPhotos)
	mux.HandleFunc("POST /api/public/start", s.handlePublicStart)
	mux.HandleFunc("POST /api/public/stop", s.handlePublicStop)
	mux.HandleFunc("GET /api/public/status", s.handlePublicStatus)
	mux.HandleFunc("GET /api/events/{id}/sync-config", s.handleGetSyncConfig)
	mux.HandleFunc("PUT /api/events/{id}/sync-config", s.handleSetSyncConfig)
	mux.HandleFunc("POST /api/events/{id}/sync", s.handleSyncPush)
	mux.HandleFunc("POST /api/events/{id}/sync-pull", s.handleSyncPull)

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
	s.live.StopAll()
	s.photos.StopAll()
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) handleLiveStart(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("id")
	store, err := s.events.Open(eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	var req struct {
		Port string `json:"port"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil && err != io.EOF {
		s.fail(w, err)
		return
	}
	if err := s.live.Start(store, eventID, req.Port); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.live.Status(eventID))
}

func (s *Server) handleLiveStop(w http.ResponseWriter, r *http.Request) {
	s.live.Stop(r.PathValue("id"))
	writeJSON(w, http.StatusOK, s.live.Status(r.PathValue("id")))
}

func (s *Server) handleLiveStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.live.Status(r.PathValue("id")))
}

func (s *Server) handleLiveFeed(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit) //nolint:errcheck
	}
	passes, err := store.ListRecentPasses(r.Context(), r.PathValue("id"), limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, passes)
}

func (s *Server) handleManualFinish(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	var req struct {
		TimeMs  *int64 `json:"time_ms"`
		CleanMs *int64 `json:"clean_ms"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		s.fail(w, err)
		return
	}
	eventID, memberID := r.PathValue("id"), r.PathValue("memberID")
	var res service.ManualFinishResult
	switch {
	case req.CleanMs != nil:
		res, err = service.ManualFinishClean(r.Context(), store, eventID, memberID, *req.CleanMs)
	case req.TimeMs != nil:
		res, err = service.ManualFinish(r.Context(), store, eventID, memberID, *req.TimeMs)
	default:
		err = fmt.Errorf("укажите время финиша (time_ms) или чистое время (clean_ms)")
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleListManualResults(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	results, err := service.ListManualFinishes(r.Context(), store, r.PathValue("id"), r.URL.Query().Get("race"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, results)
}

func (s *Server) handleDeleteManualResult(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	var resultID int64
	if _, err := fmt.Sscanf(r.PathValue("resultID"), "%d", &resultID); err != nil {
		s.fail(w, fmt.Errorf("некорректный id результата"))
		return
	}
	res, err := service.DeleteManualResult(r.Context(), store, r.PathValue("id"), resultID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleCreateCapture(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	var req struct {
		TimeMs int64 `json:"time_ms"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		s.fail(w, err)
		return
	}
	id, err := store.CreatePendingCapture(r.Context(), r.PathValue("id"), req.TimeMs)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "time_ms": req.TimeMs})
}

func (s *Server) handleListCaptures(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	captures, err := store.ListPendingCaptures(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, captures)
}

func (s *Server) handleDeleteCapture(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	var capID int64
	if _, err := fmt.Sscanf(r.PathValue("capID"), "%d", &capID); err != nil {
		s.fail(w, fmt.Errorf("некорректный id захвата"))
		return
	}
	if err := store.DeletePendingCapture(r.Context(), r.PathValue("id"), capID); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ─── Finish photos (Chrono Cam integration) ───────────────────────────────────

func (s *Server) handleListPhotoSources(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	sources, err := store.ListPhotoSources(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sources)
}

func (s *Server) handleAddPhotoSource(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("id")
	store, err := s.events.Open(eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	var req struct {
		BaseURL string `json:"base_url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		s.fail(w, err)
		return
	}
	if req.BaseURL == "" {
		s.fail(w, fmt.Errorf("укажите адрес телефона (base_url), напр. http://192.168.0.50:8080"))
		return
	}
	if err := store.UpsertPhotoSource(r.Context(), eventID, sqlite.PhotoSource{BaseURL: req.BaseURL, Enabled: true}); err != nil {
		s.fail(w, err)
		return
	}
	// Begin (idempotent) background polling and pull once now for instant feedback.
	s.photos.Start(store, eventID)
	stats, _ := s.photos.PollOnce(r.Context(), store, eventID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "polled": stats})
}

func (s *Server) handleDeletePhotoSource(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("id")
	store, err := s.events.Open(eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	baseURL := r.URL.Query().Get("base_url")
	if baseURL == "" {
		s.fail(w, fmt.Errorf("укажите base_url источника"))
		return
	}
	if err := store.DeletePhotoSource(r.Context(), eventID, baseURL); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handlePollPhotos(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("id")
	store, err := s.events.Open(eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	stats, err := s.photos.PollOnce(r.Context(), store, eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// handleMatchPhotos returns finish photos near a given time — the judge fixes a
// time and sees the photo + number. Query: time_ms (required), tolerance_ms
// (default 1500), bib (optional hint).
func (s *Server) handleMatchPhotos(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("id")
	store, err := s.events.Open(eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	var timeMs int64
	if _, err := fmt.Sscanf(r.URL.Query().Get("time_ms"), "%d", &timeMs); err != nil {
		s.fail(w, fmt.Errorf("укажите time_ms"))
		return
	}
	tolerance := int64(1500)
	// Accept both tolerance_ms (preferred) and the shorthand tolerance.
	if v := r.URL.Query().Get("tolerance_ms"); v != "" {
		fmt.Sscanf(v, "%d", &tolerance) //nolint:errcheck
	} else if v := r.URL.Query().Get("tolerance"); v != "" {
		fmt.Sscanf(v, "%d", &tolerance) //nolint:errcheck
	}
	photos, err := service.MatchPhotos(r.Context(), store, eventID, timeMs, tolerance, r.URL.Query().Get("bib"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, photos)
}

// handleRecentPhotos returns the newest finishes (desc by time) — backs the
// optional live wall. Query: limit (default 50).
func (s *Server) handleRecentPhotos(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("id")
	store, err := s.events.Open(eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit) //nolint:errcheck
	}
	photos, err := store.ListRecentPhotos(r.Context(), eventID, limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, photos)
}

// handleMergedPhotos returns the recent finishes with the same crossing seen by
// several cameras collapsed into one (authoritative server-side merge, so the wall,
// matching, and export agree). Query: limit (default 200), window_ms (merge window).
func (s *Server) handleMergedPhotos(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("id")
	store, err := s.events.Open(eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit) //nolint:errcheck
	}
	windowMs := int64(service.MergeWindowMs)
	if v := r.URL.Query().Get("window_ms"); v != "" {
		fmt.Sscanf(v, "%d", &windowMs) //nolint:errcheck
	}
	photos, err := store.ListRecentPhotos(r.Context(), eventID, limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, service.MergeFinishes(photos, windowMs))
}

// handlePhotoImage is a caching proxy: it serves a finish-photo JPEG from the
// local cache, fetching it from the phone once on a miss. The frontend points
// every <img> here instead of straight at the phone, so the smartphone and LAN
// aren't hit on every redraw. Query: u = the absolute phone image URL.
func (s *Server) handlePhotoImage(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("id")
	u := r.URL.Query().Get("u")
	if u == "" {
		s.fail(w, fmt.Errorf("укажите параметр u (адрес кадра)"))
		return
	}
	store, err := s.events.Open(eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	sources, err := store.ListPhotoSources(r.Context(), eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	allowed := make([]string, 0, len(sources))
	for _, src := range sources {
		allowed = append(allowed, src.BaseURL)
	}
	if !service.HostAllowed(u, allowed) {
		http.Error(w, "источник не зарегистрирован", http.StatusForbidden)
		return
	}
	if s.photoCache == nil {
		s.fail(w, fmt.Errorf("кэш фото недоступен"))
		return
	}
	data, err := s.photoCache.Get(r.Context(), u)
	if err != nil {
		http.Error(w, "кадр недоступен: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(data)
}

func (s *Server) handlePhotoStatus(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("id")
	store, err := s.events.Open(eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	sources, err := store.ListPhotoSources(r.Context(), eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	count, err := store.CountPhotos(r.Context(), eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	// finishes_count is the distinct crossings after merging multi-camera copies —
	// the honest "finishes" total, vs photos_count which counts every raw copy.
	finishes, err := service.CountMergedFinishes(r.Context(), store, eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"running":        s.photos.Running(eventID),
		"sources":        sources,
		"photos_count":   count,
		"finishes_count": finishes,
	})
}

// handleExportFinishesCSV writes the coordinated merged-finishes CSV (all cameras in
// one file) to the user's Downloads directory and returns its path.
func (s *Server) handleExportFinishesCSV(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	data, name, err := service.BuildMergedFinishesCSV(r.Context(), store, r.PathValue("id"))
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

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
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

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, version.Get())
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
	// Feibot CSV is zoneless local time — an explicit timezone is mandatory.
	// Defaulting to UTC would silently shift every read by the venue's offset
	// (e.g. −3h for МСК), so fail closed instead.
	tz := r.URL.Query().Get("tz")
	if tz == "" {
		s.fail(w, fmt.Errorf("укажите таймзону (tz): CSV Feibot хранит локальное время без зоны"))
		return
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
		ID                          string `json:"id"`
		Name                        string `json:"name"`
		Date                        string `json:"date"`
		Format                      string `json:"format"`
		StartedAtMs                 *int64 `json:"started_at_ms"`
		CategoryExcludesTopByGender bool   `json:"category_excludes_top_by_gender"`
	}
	out := make([]raceJSON, 0, len(races))
	for _, rc := range races {
		out = append(out, raceJSON{
			ID: rc.ID, Name: rc.Name, Date: rc.Date, Format: string(rc.Format),
			StartedAtMs:                 rc.StartedAtMs,
			CategoryExcludesTopByGender: rc.CategoryExcludesTopByGender,
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

func (s *Server) handleCreateMember(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	var req service.CreateMemberRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		s.fail(w, err)
		return
	}
	memberID, res, err := service.CreateMember(r.Context(), store, r.PathValue("id"), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"member_id":      memberID,
		"recount_needed": res.RecountNeeded,
	})
}

func (s *Server) handleGetMember(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	m, err := store.GetMember(r.Context(), r.PathValue("memberID"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": m.ID, "race_id": m.RaceID, "category_id": m.CategoryID,
		"number": m.Number, "epc": m.EPC,
		"first_name": m.FirstName, "last_name": m.LastName,
		"gender": m.Gender, "dob": m.DOB, "team": m.Team, "city": m.City,
	})
}

type categoryJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// handleListCategories returns the full event-global catalog — the source for
// the "add category to a race" picker. The member-edit dropdown uses the
// per-race attached set (handleListRaceCategories) instead.
func (s *Server) handleListCategories(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	categories, err := store.ListCategories(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]categoryJSON, 0, len(categories))
	for _, c := range categories {
		out = append(out, categoryJSON{ID: c.ID, Name: c.Name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
}

// handleListRaceCategories returns the categories attached to a race (run5's
// category_race pivot) — the set offered when assigning a participant.
func (s *Server) handleListRaceCategories(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	categories, err := store.ListRaceCategories(r.Context(), r.PathValue("raceID"))
	if err != nil {
		s.fail(w, err)
		return
	}
	out := make([]categoryJSON, 0, len(categories))
	for _, c := range categories {
		out = append(out, categoryJSON{ID: c.ID, Name: c.Name})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAttachCategory(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	var req struct {
		CategoryID string `json:"category_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		s.fail(w, err)
		return
	}
	res, err := service.AttachCategory(r.Context(), store, r.PathValue("id"), r.PathValue("raceID"), req.CategoryID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleDetachCategory(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	res, err := service.DetachCategory(r.Context(), store, r.PathValue("id"), r.PathValue("raceID"), r.PathValue("categoryID"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	members, err := store.ListMembersByEvent(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, members)
}

func (s *Server) handleCreateCheckpoint(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	var req service.CreateCheckpointRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		s.fail(w, err)
		return
	}
	id, res, err := service.CreateCheckpoint(r.Context(), store, r.PathValue("id"), req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"checkpoint_id": id, "recount_needed": res.RecountNeeded})
}

func (s *Server) handleDeleteCheckpoint(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	res, err := service.DeleteCheckpoint(r.Context(), store, r.PathValue("id"), r.PathValue("cpID"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleBackup snapshots the event database (.chrono with results and the
// edit journal) into Downloads.
func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	dir, err := service.DownloadsDir()
	if err != nil {
		s.fail(w, err)
		return
	}
	path, err := service.SnapshotEvent(r.Context(), store, r.PathValue("id"), dir)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": path})
}

// handleExportJSON writes the contract-format JSON backup into Downloads.
func (s *Server) handleExportJSON(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	data, name, err := service.BuildEventExport(r.Context(), store, r.PathValue("id"))
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

// handleGetSyncConfig returns the event's run5 sync target. The token itself is
// never echoed — only whether one is configured.
func (s *Server) handleGetSyncConfig(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	cfg, err := store.GetSyncConfig(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"base_url":       cfg.BaseURL,
		"token_set":      cfg.Token != "",
		"last_synced_at": cfg.LastSyncedAt,
	})
}

func (s *Server) handleSetSyncConfig(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	var req struct {
		BaseURL string `json:"base_url"`
		Token   string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		s.fail(w, err)
		return
	}
	if err := store.SetSyncConfig(r.Context(), r.PathValue("id"), req.BaseURL, req.Token); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSyncPush assembles the event payload and pushes it to run5.
func (s *Server) handleSyncPush(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("id")
	store, err := s.events.Open(eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	var req struct {
		Overwrite *bool `json:"overwrite"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil && err != io.EOF {
		s.fail(w, err)
		return
	}
	overwrite := req.Overwrite == nil || *req.Overwrite // default: overwrite

	cfg, err := store.GetSyncConfig(r.Context(), eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if cfg.BaseURL == "" || cfg.Token == "" {
		s.fail(w, fmt.Errorf("настройте адрес сайта и токен синхронизации"))
		return
	}

	payload, summary, err := service.BuildSyncPayload(r.Context(), store, eventID, overwrite)
	if err != nil {
		s.fail(w, err)
		return
	}
	resp, err := service.PushSync(r.Context(), cfg.BaseURL, cfg.Token, eventID, payload)
	if err != nil {
		s.fail(w, err)
		return
	}
	sum := sha256.Sum256(payload)
	if err := store.SetSyncResult(r.Context(), eventID, time.Now().UnixMilli(), hex.EncodeToString(sum[:])); err != nil {
		s.logger.Printf("save sync result: %v", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": summary, "response": resp})
}

// handleSyncPull fetches the current event export from run5 and re-imports it.
// siteWins (overwrite) skips the local-edits-win journal replay.
func (s *Server) handleSyncPull(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("id")
	store, err := s.events.Open(eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	var req struct {
		Overwrite *bool `json:"overwrite"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil && err != io.EOF {
		s.fail(w, err)
		return
	}
	siteWins := req.Overwrite != nil && *req.Overwrite // pull default: local edits win

	cfg, err := store.GetSyncConfig(r.Context(), eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if cfg.BaseURL == "" || cfg.Token == "" {
		s.fail(w, fmt.Errorf("настройте адрес сайта и токен синхронизации"))
		return
	}

	data, err := service.PullExport(r.Context(), cfg.BaseURL, cfg.Token, eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	stats, err := s.events.ImportExportOpts(r.Context(), bytes.NewReader(data), siteWins)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"imported": stats, "site_wins": siteWins})
}

// ─── Public LAN results broadcast (operator-only control) ─────────────────────
// These run on the localhost API; they switch the SEPARATE read-only publicweb
// server on/off. Only that server is reachable from the network, and only its
// GET endpoints.

func (s *Server) handlePublicStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EventID string `json:"event_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil && err != io.EOF {
		s.fail(w, err)
		return
	}
	if err := s.public.Publish(req.EventID); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.publicStatus())
}

func (s *Server) handlePublicStop(w http.ResponseWriter, _ *http.Request) {
	s.public.Unpublish()
	writeJSON(w, http.StatusOK, s.publicStatus())
}

func (s *Server) handlePublicStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.publicStatus())
}

// publicStatus wraps the broadcast status with one scannable QR data-URI per LAN
// address. The machine may expose several real NICs (Ethernet + Wi-Fi), so the
// settings screen shows every candidate with its own QR and the operator picks
// the one on the venue network — no guessing a single "primary" that might be
// the wrong (e.g. Docker) interface.
func (s *Server) publicStatus() map[string]any {
	st := s.public.Status()
	endpoints := make([]map[string]string, 0, len(st.URLs))
	if st.Running {
		for _, url := range st.URLs {
			ep := map[string]string{"url": url}
			if png, err := qrcode.Encode(url, qrcode.Medium, 320); err == nil {
				ep["qr"] = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
			}
			endpoints = append(endpoints, ep)
		}
	}
	return map[string]any{
		"running":   st.Running,
		"event_id":  st.EventID,
		"port":      st.Port,
		"endpoints": endpoints,
	}
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
