package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/service"
)

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
	overwrite := req.Overwrite == nil || *req.Overwrite

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
	siteWins := req.Overwrite != nil && *req.Overwrite

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
