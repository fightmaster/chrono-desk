package httpapi

import "net/http"

func (s *Server) handlePresenterClubsRefresh(w http.ResponseWriter, r *http.Request) {
	stats, err := s.events.RefreshPresenterClubs(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}
