package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"

	qrcode "github.com/skip2/go-qrcode"
)

func (s *Server) handlePublicStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EventID string `json:"event_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil && err != io.EOF {
		s.fail(w, err)
		return
	}
	if err := s.public.Publish(req.EventID); err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.publicStatus())
}

func (s *Server) handlePublicStop(w http.ResponseWriter, _ *http.Request) {
	s.public.Unpublish()
	writeJSON(w, http.StatusOK, s.publicStatus())
}

func (s *Server) handlePublicStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.publicStatus())
}

func (s *Server) publicStatus() map[string]any {
	st := s.public.Status()
	endpoints := make([]map[string]string, 0, len(st.URLs))
	if st.Running {
		for _, url := range st.URLs {
			ep := map[string]string{"url": url}
			if png, err := qrcode.Encode(url, qrcode.Medium, 320); err == nil {
				ep["qr"] = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
			}
			endpoints = append(endpoints, ep)
		}
	}
	return map[string]any{
		"running":   st.Running,
		"event_id":  st.EventID,
		"port":      st.Port,
		"endpoints": endpoints,
	}
}
