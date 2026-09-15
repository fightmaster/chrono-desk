package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"gitlab.com/fightmaster1/chrono-desk/internal/service"
)

func (s *Server) handlePacketIssuanceConflicts(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if len(query) > 3 {
		s.fail(w, errors.New("некорректный фильтр конфликтов"))
		return
	}
	limit, err := packetPageNumber(query.Get("limit"), 50, 1, 100)
	if err != nil {
		s.fail(w, err)
		return
	}
	offset, err := packetPageNumber(query.Get("offset"), 0, 0, 1_000_000)
	if err != nil {
		s.fail(w, err)
		return
	}
	unresolved := query.Get("view") != "all"
	if view := query.Get("view"); view != "" && view != "all" && view != "unresolved" {
		s.fail(w, errors.New("некорректный фильтр конфликтов"))
		return
	}
	store, err := s.events.Open(r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	page, err := service.ListPacketConflictCandidates(r.Context(), store, r.PathValue("id"), unresolved, limit, offset)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handlePacketIssuanceResolve(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Choice   string `json:"choice"`
		Reason   string `json:"reason"`
		Actor    string `json:"actor"`
		Evidence string `json:"evidence"`
	}
	if err := decodePacketLANRequest(w, r, &request); err != nil {
		s.fail(w, err)
		return
	}
	if request.Choice != "current" && request.Choice != "proposal" {
		s.fail(w, errors.New("выберите решение конфликта"))
		return
	}
	operation, err := service.ResolvePacketConflict(r.Context(), s.events, r.PathValue("id"),
		r.PathValue("operationID"), request.Choice == "proposal", request.Reason, request.Actor, request.Evidence)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"resolution_operation_id": operation.OperationID})
}

func packetPageNumber(raw string, fallback, min, max int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < min || value > max {
		return 0, errors.New("некорректная страница конфликтов")
	}
	return value, nil
}
