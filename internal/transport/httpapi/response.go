package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

func (s *Server) fail(w http.ResponseWriter, err error) {
	s.logger.Printf("http error: %v", err)
	status := http.StatusInternalServerError
	var maxBytes *http.MaxBytesError
	if errors.As(err, &maxBytes) {
		status = http.StatusRequestEntityTooLarge
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		_ = err
	}
}
