// Package httpapi exposes the application core over HTTP. The Wails frontend
// is its first consumer; later the same API serves the local network (judges'
// tablets) and a headless mode.
package httpapi

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/service"
	"gitlab.com/fightmaster1/chrono-desk/internal/transport/publicweb"
)

type Server struct {
	httpServer *http.Server
	listener   net.Listener
	events     *service.EventService
	live       *service.LiveManager
	photos     *service.PhotoManager
	photoCache *service.PhotoCache
	public     *publicweb.Server
	logger     *log.Logger
}

// New binds addr (use "127.0.0.1:0" for an ephemeral local port) and builds
// the route table. Call Start to begin serving. public is the read-only LAN
// results broadcaster controlled by the /api/public/* endpoints.
func New(
	addr string,
	events *service.EventService,
	live *service.LiveManager,
	photos *service.PhotoManager,
	photoCache *service.PhotoCache,
	public *publicweb.Server,
	logger *log.Logger,
	apiToken string,
) (*Server, error) {
	if strings.TrimSpace(apiToken) == "" {
		return nil, fmt.Errorf("api token is required")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}

	s := &Server{
		listener:   ln,
		events:     events,
		live:       live,
		photos:     photos,
		photoCache: photoCache,
		public:     public,
		logger:     logger,
	}

	s.httpServer = &http.Server{
		// The Wails webview loads the UI from its own origin, so the
		// localhost API must answer CORS preflight.
		Handler:           cors(requireAPIToken(apiToken, s.routes())),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s, nil
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
	s.live.StopAll()
	s.photos.StopAll()
	return s.httpServer.Shutdown(ctx)
}
