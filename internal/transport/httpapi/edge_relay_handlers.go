package httpapi

import (
	"encoding/json"
	"net/http"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

func (s *Server) handleEdgeRelayStatus(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("id")
	store, err := s.events.Open(eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	config, err := store.EdgeRelayConfig(r.Context(), eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	progress, err := store.EdgeRelayProgress(r.Context(), eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	running, lastError := s.edgeRelay.Status(eventID)
	writeJSON(w, http.StatusOK, map[string]any{"config": config, "progress": progress, "running": running, "last_error": lastError})
}

func (s *Server) handleEdgeRelayConfigure(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Endpoint       *string `json:"endpoint"`
		Enabled        *bool   `json:"enabled"`
		Revision       *int64  `json:"revision"`
		TLSBundle      string  `json:"tls_bundle"`
		ConfirmPending bool    `json:"confirm_pending"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&request); err != nil {
		s.fail(w, err)
		return
	}
	if request.Endpoint == nil || request.Enabled == nil || request.Revision == nil {
		http.Error(w, "укажите endpoint, enabled и revision явно", http.StatusBadRequest)
		return
	}
	config := domain.EdgeRelayConfig{Endpoint: *request.Endpoint, Enabled: *request.Enabled, Revision: *request.Revision, TLSBundle: request.TLSBundle}
	if _, err := s.edgeRelay.Configure(r.Context(), r.PathValue("id"), config, request.ConfirmPending); err != nil {
		s.fail(w, err)
		return
	}
	s.handleEdgeRelayStatus(w, r)
}
