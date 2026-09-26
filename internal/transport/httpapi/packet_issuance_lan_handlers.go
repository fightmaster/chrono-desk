package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"gitlab.com/fightmaster1/chrono-desk/internal/service"
)

func (s *Server) handlePacketIssuanceLANStatus(w http.ResponseWriter, r *http.Request) {
	if s.packetLAN == nil {
		s.fail(w, errors.New("локальная выдача пакетов недоступна"))
		return
	}
	status, err := s.packetLAN.Status(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handlePacketIssuanceLANStart(w http.ResponseWriter, r *http.Request) {
	if err := decodePacketLANRequest(w, r, &struct{}{}); err != nil {
		s.fail(w, err)
		return
	}
	if s.packetLAN == nil {
		s.fail(w, errors.New("локальная выдача пакетов недоступна"))
		return
	}
	if err := s.packetLAN.Start(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, err)
		return
	}
	s.handlePacketIssuanceLANStatus(w, r)
}

func (s *Server) handlePacketIssuanceLANStop(w http.ResponseWriter, r *http.Request) {
	if err := decodePacketLANRequest(w, r, &struct{}{}); err != nil {
		s.fail(w, err)
		return
	}
	if s.packetLAN == nil {
		s.fail(w, errors.New("локальная выдача пакетов недоступна"))
		return
	}
	if err := s.packetLAN.Stop(r.Context()); err != nil {
		s.fail(w, err)
		return
	}
	s.handlePacketIssuanceLANStatus(w, r)
}

func (s *Server) handlePacketIssuanceLANInvitation(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Label string `json:"label"`
	}
	if err := decodePacketLANRequest(w, r, &request); err != nil {
		s.fail(w, err)
		return
	}
	if s.packetLAN == nil {
		s.fail(w, errors.New("локальная выдача пакетов недоступна"))
		return
	}
	invitation, err := s.packetLAN.CreateInvitation(r.Context(), r.PathValue("id"), request.Label)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, invitation)
}

func (s *Server) handlePacketIssuanceLANRevoke(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Reason string `json:"reason"`
	}
	if err := decodePacketLANRequest(w, r, &request); err != nil {
		s.fail(w, err)
		return
	}
	if s.packetLAN == nil {
		s.fail(w, errors.New("локальная выдача пакетов недоступна"))
		return
	}
	if err := s.packetLAN.Revoke(r.Context(), r.PathValue("id"), r.PathValue("connectionID"), request.Reason); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"revoked": true})
}

func (s *Server) handlePacketIssuanceLANCompact(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Execute bool `json:"execute"`
	}
	if err := decodePacketLANRequest(w, r, &request); err != nil {
		s.fail(w, err)
		return
	}
	if s.packetLAN == nil {
		s.fail(w, errors.New("локальная выдача пакетов недоступна"))
		return
	}
	result, err := s.packetLAN.Compact(r.Context(), r.PathValue("id"), request.Execute)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handlePacketIssuanceLANCA(w http.ResponseWriter, _ *http.Request) {
	if s.packetLAN == nil {
		s.fail(w, errors.New("локальная выдача пакетов недоступна"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"filename": "chrono-desk-ca.crt", "certificate": string(s.packetLAN.CACertificate()),
	})
}

func (s *Server) handlePacketIssuanceLANCAExport(w http.ResponseWriter, _ *http.Request) {
	if s.packetLAN == nil {
		s.fail(w, errors.New("локальная выдача пакетов недоступна"))
		return
	}
	path, err := service.SaveToDownloads("chrono-desk-ca.crt", s.packetLAN.CACertificate())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": path})
}

func decodePacketLANRequest(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("некорректный запрос локальной выдачи")
	}
	return nil
}
