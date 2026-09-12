package httpapi

import (
	"encoding/json"
	"io"
	"net/http"

	"gitlab.com/fightmaster1/chrono-desk/internal/service"
)

func (s *Server) handlePacketIssuanceSiteStatus(w http.ResponseWriter, r *http.Request) {
	status, err := service.GetPacketRelayStatus(r.Context(), s.events, r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handlePacketIssuanceSiteConnect(w http.ResponseWriter, r *http.Request) {
	var request struct{}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil && err != io.EOF {
		s.fail(w, err)
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		s.fail(w, err)
		return
	}
	status, err := service.ConnectPacketIssuanceSite(r.Context(), s.events, r.PathValue("id"), "Chrono Desk")
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}
