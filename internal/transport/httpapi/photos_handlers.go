package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/service"
)

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
	photos, err := store.ListPhotosForMerge(r.Context(), eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	finishes := service.MergeFinishes(photos, windowMs)
	if limit > 0 && len(finishes) > limit {
		finishes = finishes[:limit]
	}
	ids := make([]string, len(finishes))
	for i := range finishes {
		ids[i] = finishes[i].ID
	}
	framesByID, err := store.GetFramesByIDs(r.Context(), eventID, ids)
	if err != nil {
		s.fail(w, err)
		return
	}
	for i := range finishes {
		if fr, ok := framesByID[finishes[i].ID]; ok {
			finishes[i].Frames = fr
		}
	}
	writeJSON(w, http.StatusOK, finishes)
}

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

func (s *Server) handleExportFinishes(w http.ResponseWriter, r *http.Request) {
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	data, name, err := service.BuildMergedFinishesZip(r.Context(), store, s.photoCache, r.PathValue("id"))
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
