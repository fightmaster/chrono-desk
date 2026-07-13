package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/service"
	"gitlab.com/fightmaster1/chrono-desk/internal/transport/httpapi"
	"gitlab.com/fightmaster1/chrono-desk/internal/transport/publicweb"
)

// App is the only struct bound to the Wails frontend. The UI talks to the Go
// core exclusively over the embedded HTTP API (pattern from RaceTorchApp);
// bootstrap bindings hand it the localhost URL and per-process API token.
type App struct {
	ctx      context.Context
	api      *httpapi.Server
	public   *publicweb.Server
	events   *service.EventService
	apiToken string
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	logger := log.Default()

	catalog, err := sqlite.NewEventCatalog(dataDir())
	if err != nil {
		log.Fatalf("init event catalog: %v", err)
	}
	events := service.NewEventService(catalog, logger)
	a.events = events

	// Read-only LAN results broadcast (off until the operator turns it on). It
	// gets its own server so only GET endpoints ever reach the network.
	a.public = publicweb.New(events, logger, publicPort())

	live := service.NewLiveManager(logger)
	photos := service.NewPhotoManager(logger)
	photoCache := newPhotoCache(logger)

	apiToken, err := newAPIToken()
	if err != nil {
		log.Fatalf("generate api token: %v", err)
	}
	api, err := httpapi.New("127.0.0.1:0", events, live, photos, photoCache, a.public, logger, apiToken)
	if err != nil {
		log.Fatalf("start http api: %v", err)
	}
	a.api = api
	a.apiToken = apiToken
	go func() {
		if err := api.Start(); err != nil && err != http.ErrServerClosed {
			log.Printf("http api stopped: %v", err)
		}
	}()
	log.Printf("http api listening on %s, events in %s", api.BaseURL(), dataDir())
}

func (a *App) shutdown(_ context.Context) {
	if a.public != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = a.public.Shutdown(ctx)
	}
	if a.api != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = a.api.Shutdown(ctx)
	}
	if a.events != nil {
		a.events.Close()
	}
}

// APIBaseURL returns the embedded HTTP API address for the frontend.
func (a *App) APIBaseURL() string {
	return a.api.BaseURL()
}

// APIToken returns the per-process credential used only by the embedded
// frontend when it calls the localhost control API. It is never used by the
// separate tokenless LAN results broadcast.
func (a *App) APIToken() string {
	return a.apiToken
}

func newAPIToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func newPhotoCache(logger *log.Logger) *service.PhotoCache {
	cacheRoot, err := os.UserCacheDir()
	if err != nil || cacheRoot == "" {
		cacheRoot = os.TempDir()
	}
	pc, err := service.NewPhotoCache(filepath.Join(cacheRoot, "chrono-desk", "photos"))
	if err != nil {
		logger.Printf("photo cache disabled: %v", err)
		return nil
	}
	return pc
}

// publicPort is the LAN port for the read-only results broadcast.
// CHRONO_PUBLIC_PORT overrides the default; an invalid value falls back.
func publicPort() int {
	if v := os.Getenv("CHRONO_PUBLIC_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 && p <= 65535 {
			return p
		}
	}
	return publicweb.DefaultPort
}

// dataDir resolves where event .chrono files live. CHRONO_DATA_DIR overrides
// the per-user default.
func dataDir() string {
	if dir := os.Getenv("CHRONO_DATA_DIR"); dir != "" {
		return dir
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base = "."
	}
	return filepath.Join(base, "chrono-desk", "events")
}
