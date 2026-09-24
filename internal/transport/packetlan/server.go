// Package packetlan exposes only the packet-issuance contract over an opt-in
// HTTPS LAN listener. It never shares the localhost control token or routes.
package packetlan

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"
	qrcode "github.com/skip2/go-qrcode"
	"gitlab.com/fightmaster1/chrono-desk/internal/credentials"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/packetissuance"
	"gitlab.com/fightmaster1/chrono-desk/internal/service"
)

const (
	DefaultPort = 8443
	apiPath     = "/api/packet-issuance/v1"
)

type advertisement interface{ Shutdown() }

type Server struct {
	events    *service.EventService
	receiver  *service.PacketIssuanceReceiver
	logger    *log.Logger
	tls       credentials.PacketLANTLS
	port      int
	pwaURL    string
	origins   map[string]bool
	now       func() time.Time
	advertise func(int, string) (advertisement, error)

	mu         sync.Mutex
	httpServer *http.Server
	listener   net.Listener
	mdns       advertisement
	eventID    string
	actualPort int
	claims     map[string]claimWindow
}

type claimWindow struct {
	started time.Time
	count   int
}

type Status struct {
	Running       bool                              `json:"running"`
	EventID       string                            `json:"event_id,omitempty"`
	Port          int                               `json:"port"`
	APIBaseURL    string                            `json:"api_base_url,omitempty"`
	CAFingerprint string                            `json:"ca_fingerprint"`
	Connections   []sqlite.PacketLANConnection      `json:"connections"`
	Retention     service.PacketFeedRetentionResult `json:"retention"`
}

type Invitation struct {
	ConnectionID  string `json:"connection_id"`
	ExpiresAt     string `json:"expires_at"`
	InvitationURL string `json:"invitation_url"`
	QRCode        string `json:"qr_code"`
}

func New(events *service.EventService, logger *log.Logger, dataDir string, port int, pwaURL string, origins []string) (*Server, error) {
	if port < 0 || port > 65535 {
		return nil, errors.New("invalid packet LAN port")
	}
	if port == 0 {
		port = DefaultPort
	}
	parsedPWA, err := url.Parse(pwaURL)
	if err != nil || parsedPWA.Scheme != "https" || parsedPWA.Host == "" || parsedPWA.User != nil || parsedPWA.RawQuery != "" || parsedPWA.Fragment != "" {
		return nil, errors.New("invalid packet issuance PWA URL")
	}
	allowed := map[string]bool{}
	for _, value := range origins {
		parsed, err := url.Parse(strings.TrimSpace(value))
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, fmt.Errorf("invalid packet PWA origin %q", value)
		}
		allowed[parsed.Scheme+"://"+parsed.Host] = true
	}
	if len(allowed) == 0 {
		return nil, errors.New("at least one packet PWA origin is required")
	}
	material, err := credentials.LoadOrCreatePacketLAN(dataDir, events.InstallationID())
	if err != nil {
		return nil, err
	}
	s := &Server{events: events, receiver: service.NewPacketIssuanceReceiver(), logger: logger,
		tls: material, port: port, pwaURL: strings.TrimRight(pwaURL, "/"), origins: allowed, now: time.Now,
		claims: make(map[string]claimWindow)}
	s.advertise = advertiseMDNS
	return s, nil
}

func (s *Server) Resume(ctx context.Context) error {
	eventID, enabled, err := s.events.ActivePacketLAN(ctx)
	if err != nil || !enabled {
		return err
	}
	return s.start(ctx, eventID, false)
}

func (s *Server) Start(ctx context.Context, eventID string) error { return s.start(ctx, eventID, true) }

func (s *Server) start(ctx context.Context, eventID string, persist bool) error {
	if eventID == "" {
		return errors.New("укажите событие для локальной выдачи")
	}
	if _, err := service.PacketIssuanceBootstrap(ctx, s.events, eventID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.httpServer != nil {
		if s.eventID == eventID {
			return nil
		}
		return errors.New("сначала остановите локальную выдачу другого события")
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", s.port))
	if err != nil {
		return fmt.Errorf("не удалось открыть HTTPS-порт выдачи %d: %w", s.port, err)
	}
	actualPort := listener.Addr().(*net.TCPAddr).Port
	mdns, err := s.advertise(actualPort, s.events.InstallationID())
	if err != nil {
		listener.Close()
		return fmt.Errorf("не удалось объявить chrono-desk.local: %w", err)
	}
	if persist {
		if err := s.events.EnablePacketLAN(ctx, eventID); err != nil {
			mdns.Shutdown()
			listener.Close()
			return err
		}
	}
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	httpServer := &http.Server{Handler: s.routes(), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 20 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 16 << 10, Protocols: protocols}
	s.eventID, s.actualPort, s.listener, s.mdns, s.httpServer = eventID, actualPort, listener, mdns, httpServer
	go func() {
		err := httpServer.Serve(tls.NewListener(listener, s.tls.Config.Clone()))
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Printf("packet issuance LAN stopped: %v", err)
		}
		s.mu.Lock()
		if s.httpServer == httpServer {
			mdns := s.mdns
			s.httpServer, s.listener, s.mdns, s.eventID, s.actualPort = nil, nil, nil, "", 0
			s.mu.Unlock()
			if mdns != nil {
				mdns.Shutdown()
			}
			return
		}
		s.mu.Unlock()
	}()
	s.logger.Printf("packet issuance HTTPS LAN on %s:%d for event %s", credentials.PacketLANHostname, actualPort, eventID)
	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	if err := s.shutdown(ctx); err != nil {
		return err
	}
	return s.events.DisablePacketLAN(ctx)
}

func (s *Server) Shutdown(ctx context.Context) error { return s.shutdown(ctx) }

func (s *Server) shutdown(ctx context.Context) error {
	s.mu.Lock()
	server, mdns := s.httpServer, s.mdns
	s.httpServer, s.listener, s.mdns, s.eventID, s.actualPort = nil, nil, nil, "", 0
	s.mu.Unlock()
	if mdns != nil {
		mdns.Shutdown()
	}
	if server == nil {
		return nil
	}
	return server.Shutdown(ctx)
}

func (s *Server) Status(ctx context.Context, eventID string) (Status, error) {
	s.mu.Lock()
	status := Status{Running: s.httpServer != nil && s.eventID == eventID, EventID: s.eventID,
		Port: s.actualPort, CAFingerprint: s.tls.CAFingerprint}
	if status.Running {
		status.APIBaseURL = fmt.Sprintf("https://%s:%d%s", credentials.PacketLANHostname, s.actualPort, apiPath)
	}
	s.mu.Unlock()
	connections, err := s.events.ListPacketLANConnections(ctx, eventID)
	if err != nil {
		return Status{}, err
	}
	status.Connections = connections
	store, err := s.events.Open(eventID)
	if err != nil {
		return Status{}, err
	}
	status.Retention, err = service.CompactPacketIssuanceFeed(ctx, store, eventID, 10_000, false, s.now())
	if err != nil {
		return Status{}, err
	}
	return status, nil
}

func (s *Server) CACertificate() []byte { return append([]byte(nil), s.tls.CACertificate...) }

func (s *Server) CreateInvitation(ctx context.Context, eventID, label string) (Invitation, error) {
	s.mu.Lock()
	if s.httpServer == nil || s.eventID != eventID {
		s.mu.Unlock()
		return Invitation{}, errors.New("сначала включите локальную выдачу для события")
	}
	port := s.actualPort
	s.mu.Unlock()
	store, err := s.events.Open(eventID)
	if err != nil {
		return Invitation{}, err
	}
	scope, err := store.GetPacketIssuanceScope(ctx, eventID)
	if err != nil || scope.ScopeID == "" {
		return Invitation{}, errors.New("packet issuance roster is not installed")
	}
	created, err := s.events.CreatePacketLANInvitation(ctx, eventID, scope.ScopeID, label, s.now())
	if err != nil {
		return Invitation{}, err
	}
	payload := struct {
		Version      int    `json:"version"`
		APIBaseURL   string `json:"apiBaseUrl"`
		ConnectionID string `json:"connectionId"`
		PairingCode  string `json:"pairingCode"`
	}{Version: 1, APIBaseURL: fmt.Sprintf("https://%s:%d%s", credentials.PacketLANHostname, port, apiPath),
		ConnectionID: created.ConnectionID, PairingCode: created.PairingCode}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Invitation{}, err
	}
	invitationURL := s.pwaURL + "/#issuance-connect=" + url.PathEscape(string(raw))
	png, err := qrcode.Encode(invitationURL, qrcode.Medium, 320)
	if err != nil {
		return Invitation{}, err
	}
	return Invitation{ConnectionID: created.ConnectionID,
		ExpiresAt: created.ExpiresAt.Format("2006-01-02T15:04:05.000Z"), InvitationURL: invitationURL,
		QRCode: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)}, nil
}

func (s *Server) Revoke(ctx context.Context, eventID, connectionID, reason string) error {
	return s.events.RevokePacketLAN(ctx, eventID, connectionID, reason, s.now())
}

func (s *Server) Compact(ctx context.Context, eventID string, execute bool) (service.PacketFeedRetentionResult, error) {
	s.mu.Lock()
	running := s.httpServer != nil && s.eventID == eventID
	s.mu.Unlock()
	if execute && running {
		return service.PacketFeedRetentionResult{}, errors.New("сначала остановите локальную выдачу")
	}
	now := s.now()
	connections, err := s.events.ListPacketLANConnections(ctx, eventID)
	if err != nil {
		return service.PacketFeedRetentionResult{}, err
	}
	if execute {
		for _, connection := range connections {
			activeClaim := connection.ClaimedAt != nil && connection.RevokedAt == nil && connection.ExpiresAt.After(now)
			activeInvitation := connection.ClaimedAt == nil && connection.RevokedAt == nil && connection.InvitationExpiry.After(now)
			if activeClaim || activeInvitation {
				return service.PacketFeedRetentionResult{}, errors.New("отзовите действующие подключения планшетов перед архивацией")
			}
		}
	}
	store, err := s.events.Open(eventID)
	if err != nil {
		return service.PacketFeedRetentionResult{}, err
	}
	return service.CompactPacketIssuanceFeed(ctx, store, eventID, 10_000, execute, now)
}

func (s *Server) activeEvent() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.eventID
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		s.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST "+apiPath+"/connections/{connectionID}/claim", s.handleClaim)
	mux.HandleFunc("GET "+apiPath+"/connections/{connectionID}", s.handleDescriptor)
	mux.HandleFunc("GET "+apiPath+"/connections/{connectionID}/bootstrap", s.handleBootstrap)
	mux.HandleFunc("POST "+apiPath+"/connections/{connectionID}/operations", s.handleOperations)
	mux.HandleFunc("GET "+apiPath+"/connections/{connectionID}/feed", s.handleFeed)
	return s.cors(mux)
}

func (s *Server) handleClaim(w http.ResponseWriter, r *http.Request) {
	if !s.allowClaim(r.RemoteAddr, r.PathValue("connectionID"), s.now()) {
		s.writeError(w, http.StatusTooManyRequests, "claim_rate_limited")
		return
	}
	if !jsonRequest(r) {
		s.writeError(w, http.StatusUnsupportedMediaType, "invalid_content_type")
		return
	}
	var request struct {
		PairingCode      string `json:"pairingCode"`
		OriginInstanceID string `json:"originInstanceId"`
		Credential       string `json:"credential"`
	}
	if err := decodeExact(w, r, 4096, &request); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_claim")
		return
	}
	connection, err := s.events.ClaimPacketLAN(r.Context(), r.PathValue("connectionID"), request.PairingCode,
		request.OriginInstanceID, request.Credential, s.now())
	if err != nil || connection.EventID != s.activeEvent() {
		s.writeError(w, http.StatusUnauthorized, "connection_unavailable")
		return
	}
	s.writeJSON(w, http.StatusOK, descriptor(connection))
}

func (s *Server) authenticate(r *http.Request) (sqlite.PacketLANConnection, error) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") || strings.Contains(header[7:], " ") {
		return sqlite.PacketLANConnection{}, errors.New("missing credential")
	}
	connection, err := s.events.AuthenticatePacketLAN(r.Context(), r.PathValue("connectionID"), header[7:], s.now())
	if err != nil || connection.EventID != s.activeEvent() {
		return sqlite.PacketLANConnection{}, errors.New("connection unavailable")
	}
	return connection, nil
}

func (s *Server) handleDescriptor(w http.ResponseWriter, r *http.Request) {
	connection, err := s.authenticate(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "connection_unavailable")
		return
	}
	s.writeJSON(w, http.StatusOK, descriptor(connection))
}

func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	connection, err := s.authenticate(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "connection_unavailable")
		return
	}
	bootstrap, err := service.PacketIssuanceBootstrap(r.Context(), s.events, connection.EventID, r.URL.Query().Get("reserveOrigins") == "1")
	if err != nil || bootstrap.ScopeID != connection.ScopeID {
		s.writeError(w, http.StatusConflict, "roster_unavailable")
		return
	}
	s.writeJSON(w, http.StatusOK, bootstrap)
}

func (s *Server) handleOperations(w http.ResponseWriter, r *http.Request) {
	if !jsonRequest(r) {
		s.writeError(w, http.StatusUnsupportedMediaType, "invalid_content_type")
		return
	}
	connection, err := s.authenticate(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "connection_unavailable")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		s.writeError(w, http.StatusRequestEntityTooLarge, "invalid_operation_batch")
		return
	}
	operations, err := packetissuance.ParseBatch(body)
	if err != nil {
		s.writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	store, err := s.events.Open(connection.EventID)
	if err != nil {
		s.writeError(w, http.StatusConflict, "event_unavailable")
		return
	}
	receipts, err := s.receiver.Receive(r.Context(), store, service.PacketConnectionContext{
		ConnectionID: connection.ConnectionID, EventID: connection.EventID, ScopeID: connection.ScopeID,
		OriginInstanceID: connection.OriginInstanceID,
	}, operations)
	if err != nil {
		s.writeError(w, http.StatusConflict, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"schemaVersion": 1, "receipts": receipts})
}

func (s *Server) handleFeed(w http.ResponseWriter, r *http.Request) {
	connection, err := s.authenticate(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "connection_unavailable")
		return
	}
	query := r.URL.Query()
	if len(query["after"]) > 1 || len(query["limit"]) > 1 || len(query) > 2 {
		s.writeError(w, http.StatusUnprocessableEntity, "invalid_feed_query")
		return
	}
	after := "0"
	if len(query["after"]) == 1 {
		after = query["after"][0]
	}
	limit := 100
	if value := query.Get("limit"); value != "" {
		limit, err = strconv.Atoi(value)
	}
	if err != nil {
		s.writeError(w, http.StatusUnprocessableEntity, "invalid_feed_query")
		return
	}
	store, err := s.events.Open(connection.EventID)
	if err != nil {
		s.writeError(w, http.StatusConflict, "event_unavailable")
		return
	}
	page, err := service.PacketIssuanceFeedPage(r.Context(), store, connection.EventID, connection.ScopeID, after, limit)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidPacketFeedQuery):
			s.writeError(w, http.StatusUnprocessableEntity, "invalid_feed_query")
		case errors.Is(err, service.ErrPacketFeedCursorAhead):
			s.writeError(w, http.StatusConflict, "cursor_ahead")
		case errors.Is(err, service.ErrPacketFeedCursorExpired):
			s.writeError(w, http.StatusConflict, "cursor_expired")
		case errors.Is(err, service.ErrPacketFeedScope):
			s.writeError(w, http.StatusConflict, "scope_mismatch")
		default:
			s.logger.Printf("packet issuance LAN feed failed: %v", err)
			s.writeError(w, http.StatusInternalServerError, "feed_unavailable")
		}
		return
	}
	s.writeJSON(w, http.StatusOK, page)
}

type connectionDescriptor struct {
	Version          int    `json:"version"`
	ConnectionID     string `json:"connectionId"`
	ScopeID          string `json:"scopeId"`
	EventID          string `json:"eventId"`
	Label            string `json:"label"`
	OriginInstanceID string `json:"originInstanceId"`
	ExpiresAt        string `json:"expiresAt"`
	Receiver         struct {
		Bootstrap  bool `json:"bootstrap"`
		Operations bool `json:"operations"`
		Feed       bool `json:"feed"`
	} `json:"receiver"`
}

func descriptor(connection sqlite.PacketLANConnection) connectionDescriptor {
	result := connectionDescriptor{Version: 1, ConnectionID: connection.ConnectionID, ScopeID: connection.ScopeID,
		EventID: connection.EventID, Label: connection.Label, OriginInstanceID: connection.OriginInstanceID,
		ExpiresAt: connection.ExpiresAt.UTC().Format("2006-01-02T15:04:05.000Z")}
	result.Receiver.Bootstrap, result.Receiver.Operations, result.Receiver.Feed = true, true, true
	return result
}

func jsonRequest(r *http.Request) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
}

func (s *Server) allowClaim(remote, connectionID string, now time.Time) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	if len(host) > 128 {
		host = host[:128]
	}
	key := host + "\x00" + connectionID
	s.mu.Lock()
	defer s.mu.Unlock()
	window, found := s.claims[key]
	if !found || now.Sub(window.started) >= time.Minute || now.Before(window.started) {
		if len(s.claims) >= 1024 {
			for candidate, value := range s.claims {
				if now.Sub(value.started) >= time.Minute || now.Before(value.started) {
					delete(s.claims, candidate)
				}
			}
		}
		if len(s.claims) >= 2048 {
			return false
		}
		window = claimWindow{started: now}
	}
	if window.count >= 10 {
		return false
	}
	window.count++
	s.claims[key] = window
	return true
}

func decodeExact(w http.ResponseWriter, r *http.Request, limit int64, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		origin := r.Header.Get("Origin")
		if origin != "" {
			if !s.origins[origin] {
				s.writeError(w, http.StatusForbidden, "origin_not_allowed")
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			if origin == "" || !validPreflight(r) || !validPreflightTarget(r) {
				s.writeError(w, http.StatusForbidden, "preflight_not_allowed")
				return
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func validPreflightTarget(r *http.Request) bool {
	method := r.Header.Get("Access-Control-Request-Method")
	prefix := apiPath + "/connections/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		return false
	}
	tail := strings.Split(strings.TrimPrefix(r.URL.Path, prefix), "/")
	if len(tail) == 1 {
		return method == http.MethodGet
	}
	if len(tail) != 2 {
		return false
	}
	return (tail[1] == "claim" || tail[1] == "operations") && method == http.MethodPost ||
		(tail[1] == "bootstrap" || tail[1] == "feed") && method == http.MethodGet
}

func validPreflight(r *http.Request) bool {
	method := r.Header.Get("Access-Control-Request-Method")
	if method != http.MethodGet && method != http.MethodPost {
		return false
	}
	for _, header := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
		header = strings.ToLower(strings.TrimSpace(header))
		if header != "" && header != "authorization" && header != "content-type" && header != "accept" {
			return false
		}
	}
	return true
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) writeError(w http.ResponseWriter, status int, code string) {
	s.writeJSON(w, status, map[string]string{"error": code})
}

func advertiseMDNS(port int, installationID string) (advertisement, error) {
	ips := packetLANIPs()
	if len(ips) == 0 {
		return nil, errors.New("нет доступного LAN-адреса")
	}
	name := "Chrono Desk"
	if len(installationID) >= 8 {
		name += " " + installationID[:8]
	}
	return zeroconf.RegisterProxy(name, "_https._tcp", "local.", port, credentials.PacketLANHostname+".", ips,
		[]string{"path=" + apiPath, "contract=packet-issuance-v1"}, nil)
}

func packetLANIPs() []string {
	interfaces, _ := net.Interfaces()
	seen := map[string]bool{}
	result := []string{}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, _ := iface.Addrs()
		for _, address := range addresses {
			host, _, err := net.ParseCIDR(address.String())
			if err != nil || host.IsLoopback() || host.IsUnspecified() || host.IsLinkLocalUnicast() {
				continue
			}
			value := host.String()
			if !seen[value] {
				seen[value] = true
				result = append(result, value)
			}
		}
	}
	sort.Strings(result)
	return result
}
