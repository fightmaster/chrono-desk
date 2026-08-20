package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"gitlab.com/fightmaster1/chrono-desk/internal/service"
)

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
	s.syncPull.Start(eventID)
	writeJSON(w, http.StatusOK, s.live.Status(eventID))
}

func (s *Server) handleLiveStop(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("id")
	s.syncPull.Stop(eventID)
	s.live.Stop(eventID)
	writeJSON(w, http.StatusOK, s.live.Status(eventID))
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
