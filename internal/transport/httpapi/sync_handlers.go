package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
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
	identity := service.CurrentProjectionEvidenceIdentity()
	parity, err := store.GetProjectionEvidenceParity(r.Context(), r.PathValue("id"), identity)
	if err != nil {
		s.fail(w, err)
		return
	}
	parityWindows, err := store.ListProjectionEvidenceParity(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	storage, err := s.events.StorageStats(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"base_url":                    cfg.BaseURL,
		"token_set":                   cfg.Token != "",
		"last_synced_at":              cfg.LastSyncedAt,
		"projection_evidence":         parity,
		"projection_evidence_windows": parityWindows,
		"storage":                     storage,
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

	batch, err := store.PrepareObservationBatch(r.Context(), eventID, 20_000, time.Now())
	if err != nil {
		s.fail(w, err)
		return
	}
	payload, summary, err := service.BuildSyncPayloadV3(r.Context(), store, eventID, overwrite, batch)
	if err != nil {
		s.fail(w, err)
		return
	}
	resp, err := service.PushSync(r.Context(), cfg.BaseURL, cfg.Token, eventID, payload)
	if err != nil {
		s.fail(w, err)
		return
	}
	if batch != nil {
		if err := applyObservationAck(r.Context(), store, batch, resp.ObservationAck); err != nil {
			s.fail(w, err)
			return
		}
	}
	sum := sha256.Sum256(payload)
	if err := store.SetSyncResult(r.Context(), eventID, time.Now().UnixMilli(), hex.EncodeToString(sum[:])); err != nil {
		s.logger.Printf("save sync result: %v", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": summary, "response": resp.Summary})
}

func applyObservationAck(ctx context.Context, store *sqlite.Store, batch *sqlite.ObservationBatch, ack *service.ObservationAck) error {
	if ack == nil || ack.BatchID != batch.BatchID || ack.OriginInstanceID != batch.OriginInstanceID {
		return fmt.Errorf("сайт не подтвердил отправленный observation batch")
	}
	hasRejected := false
	items := make([]sqlite.ObservationOutboxAck, 0, len(ack.Items))
	for _, item := range ack.Items {
		hasRejected = hasRejected || item.Status == "rejected"
		items = append(items, sqlite.ObservationOutboxAck{
			ObservationID: item.ID, OriginSequence: item.OriginSequence,
			Status: item.Status, Reason: item.Reason,
		})
	}
	if hasRejected {
		if ack.AcceptedThroughSequence != nil {
			return fmt.Errorf("сайт вернул противоречивый acknowledgement watermark")
		}
	} else if ack.AcceptedThroughSequence == nil || *ack.AcceptedThroughSequence != batch.LastSequence {
		return fmt.Errorf("сайт вернул неполный acknowledgement watermark")
	}
	return store.ApplyObservationAck(ctx, batch.BatchID, items, time.Now())
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
	pulled, err := s.syncPull.PullNow(r.Context(), eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if pulled.Recount == nil {
		recount, err := service.NewRecounter(store, s.logger, false).Recount(r.Context(), eventID, "")
		if err != nil {
			s.fail(w, err)
			return
		}
		pulled.Recount = &recount
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"imported": stats, "changes": pulled.Changes, "recount": pulled.Recount, "site_wins": siteWins,
	})
}
