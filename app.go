package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/service"
	"gitlab.com/fightmaster1/chrono-desk/internal/transport/httpapi"
)

// App is the only struct bound to the Wails frontend. The UI talks to the Go
// core exclusively over the embedded HTTP API (pattern from RaceTorchApp);
// the single binding below hands the frontend its base URL.
type App struct {
	ctx    context.Context
	api    *httpapi.Server
	events *service.EventManager
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	logger := log.Default()

	events, err := service.NewEventManager(dataDir(), logger)
	if err != nil {
		log.Fatalf("init event manager: %v", err)
	}
	a.events = events

	api, err := httpapi.New("127.0.0.1:0", events, logger)
	if err != nil {
		log.Fatalf("start http api: %v", err)
	}
	a.api = api
	go func() {
		if err := api.Start(); err != nil && err != http.ErrServerClosed {
			log.Printf("http api stopped: %v", err)
		}
	}()
	log.Printf("http api listening on %s, events in %s", api.BaseURL(), dataDir())
}

func (a *App) shutdown(_ context.Context) {
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
