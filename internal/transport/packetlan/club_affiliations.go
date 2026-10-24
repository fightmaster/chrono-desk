package packetlan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"gitlab.com/fightmaster1/chrono-desk/internal/service"
)

func (s *Server) handleClubAffiliations(w http.ResponseWriter, r *http.Request) {
	connection, err := s.authenticate(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "connection_unavailable")
		return
	}
	store, err := s.events.Open(connection.EventID)
	if err != nil {
		s.writeError(w, http.StatusConflict, "event_unavailable")
		return
	}
	context := service.PacketConnectionContext{ConnectionID: connection.ConnectionID, EventID: connection.EventID, ScopeID: connection.ScopeID, OriginInstanceID: connection.OriginInstanceID}
	if r.Method == http.MethodGet {
		if len(r.URL.Query()) != 0 {
			s.writeError(w, http.StatusUnprocessableEntity, "invalid_club_query")
			return
		}
		result, err := service.ReadPacketClubAffiliations(r.Context(), store, context)
		if err != nil {
			s.writeError(w, http.StatusConflict, "club_affiliations_unavailable")
			return
		}
		body, err := json.Marshal(result)
		if err != nil {
			s.writeError(w, http.StatusConflict, "club_affiliations_unavailable")
			return
		}
		digest := sha256.Sum256(body)
		etag := `"club-` + hex.EncodeToString(digest[:]) + `"`
		w.Header().Set("ETag", etag)
		w.Header().Set("Access-Control-Expose-Headers", "ETag")
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		return
	}
	if !jsonRequest(r) {
		s.writeError(w, http.StatusUnsupportedMediaType, "invalid_content_type")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil {
		s.writeError(w, http.StatusRequestEntityTooLarge, "invalid_club_operation")
		return
	}
	receipt, err := service.ApplyPacketClubAffiliation(r.Context(), store, context, body)
	if err != nil {
		status := http.StatusConflict
		code := "club_affiliation_failed"
		if errors.Is(err, service.ErrInvalidClubOperation) {
			status = http.StatusUnprocessableEntity
			code = "invalid_club_operation"
		} else if errors.Is(err, service.ErrClubIdentityConflict) {
			code = "club_identity_conflict"
		}
		s.writeError(w, status, code)
		return
	}
	s.writeJSON(w, http.StatusOK, receipt)
}
