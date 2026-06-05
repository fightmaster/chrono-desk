// Package httpapi exposes the application core over HTTP. The Wails frontend
// is its first consumer; later the same API serves the local network (judges'
// tablets) and a headless mode.
package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

type Server struct {
	httpServer *http.Server
	listener   net.Listener
}

// New binds addr (use "127.0.0.1:0" for an ephemeral local port) and builds
// the route table. Call Start to begin serving.
func New(addr string) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handleHealth)

	return &Server{
		httpServer: &http.Server{
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		},
		listener: ln,
	}, nil
}

// BaseURL returns the address the frontend should talk to.
func (s *Server) BaseURL() string {
	return "http://" + s.listener.Addr().String()
}

// Start serves until Shutdown; it returns http.ErrServerClosed on clean stop.
func (s *Server) Start() error {
	return s.httpServer.Serve(s.listener)
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
