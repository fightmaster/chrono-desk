package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

func (s *Server) handleEdgeConfig(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("id")
	store, err := s.events.Open(eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	bindings, err := store.EdgeBindings(r.Context(), eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	pending, err := store.EdgePendingCount(r.Context(), eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	automatic, err := store.AutomaticFeibotInput(r.Context(), eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bindings": bindings, "relay_pending": pending, "automatic_feibot": automatic})
}

func (s *Server) handleEdgeConfigure(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("id")
	store, err := s.events.Open(eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	var request struct {
		Bindings []domain.EdgeBinding `json:"bindings"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024)).Decode(&request); err != nil {
		s.fail(w, err)
		return
	}
	if request.Bindings == nil {
		s.fail(w, fmt.Errorf("укажите список bindings явно; пустой список отключает привязки"))
		return
	}
	if err := s.live.ConfigureEdge(r.Context(), store, eventID, request.Bindings); err != nil {
		s.fail(w, err)
		return
	}
	s.handleEdgeConfig(w, r)
}

func (s *Server) handleEdgeStart(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("id")
	store, err := s.events.Open(eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	var request struct {
		Port     string `json:"port"`
		Combined bool   `json:"combined"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&request); err != nil && err != io.EOF {
		s.fail(w, err)
		return
	}
	start := s.live.StartEdge
	if request.Combined {
		start = s.live.StartCombined
	}
	if err := start(store, eventID, request.Port); err != nil {
		s.fail(w, err)
		return
	}
	s.syncPull.Start(eventID)
	writeJSON(w, http.StatusOK, s.live.Status(eventID))
}

func (s *Server) handleEdgeStop(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("id")
	s.live.StopEdge(eventID)
	status := s.live.Status(eventID)
	if !status.AnyRunning {
		s.syncPull.Stop(eventID)
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleEdgeJournal(w http.ResponseWriter, r *http.Request) {
	eventID := r.PathValue("id")
	store, err := s.events.Open(eventID)
	if err != nil {
		s.fail(w, err)
		return
	}
	after := int64(0)
	if value := r.URL.Query().Get("after"); value != "" {
		after, err = strconv.ParseInt(value, 10, 64)
		if err != nil || after < 0 {
			s.fail(w, fmt.Errorf("некорректный курсор журнала"))
			return
		}
	}
	items, err := store.EdgeJournal(r.Context(), eventID, after, 500)
	if err != nil {
		s.fail(w, err)
		return
	}
	next := after
	if len(items) > 0 {
		next = items[len(items)-1].Sequence
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_after": next, "may_have_more": len(items) == 500})
}
