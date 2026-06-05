package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/transport/httpapi"
)

// App is the only struct bound to the Wails frontend. The UI talks to the Go
// core exclusively over the embedded HTTP API (pattern from RaceTorchApp);
// the single binding below hands the frontend its base URL.
type App struct {
	ctx context.Context
	api *httpapi.Server
}

func NewApp() *App {
	return &App{}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx

	api, err := httpapi.New("127.0.0.1:0")
	if err != nil {
		log.Fatalf("start http api: %v", err)
	}
	a.api = api
	go func() {
		if err := api.Start(); err != nil && err != http.ErrServerClosed {
			log.Printf("http api stopped: %v", err)
		}
	}()
	log.Printf("http api listening on %s", api.BaseURL())
}

func (a *App) shutdown(_ context.Context) {
	if a.api == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = a.api.Shutdown(ctx)
}

// APIBaseURL returns the embedded HTTP API address for the frontend.
func (a *App) APIBaseURL() string {
	return a.api.BaseURL()
}
