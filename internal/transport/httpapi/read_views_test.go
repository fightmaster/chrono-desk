package httpapi

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/service"
)

func TestInteractiveViewsKeepCommittedResultsWhileWriterIsBusy(t *testing.T) {
	srv := startTestServer(t)
	fixture, err := os.ReadFile("../../service/testdata/event-export.json")
	if err != nil {
		t.Fatal(err)
	}
	var imported service.ImportStats
	decodeBody(t, mustPost(t, srv.BaseURL()+"/api/events/import", "application/json", strings.NewReader(string(fixture))), &imported)
	store, err := srv.events.Open("ev-100")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.NewRecounter(store, nil, false).Recount(context.Background(), "ev-100", ""); err != nil {
		t.Fatal(err)
	}
	paths := []string{
		"/api/events",
		"/api/events/ev-100/races",
		"/api/events/ev-100/members",
		"/api/events/ev-100/checkpoints",
		"/api/events/ev-100/categories",
		"/api/events/ev-100/races/race-10k/categories",
		"/api/events/ev-100/captures",
		"/api/events/ev-100/photos/recent",
		"/api/events/ev-100/photos/merged",
		"/api/events/ev-100/photos/status",
		"/api/events/ev-100/photos/sources",
		"/api/events/ev-100/members/mem-1/passes?scope=accepted",
		"/api/events/ev-100/members/mem-1",
		"/api/events/ev-100/races/race-10k/protocol",
		"/api/events/ev-100/live/feed?limit=50",
	}
	baseline := make(map[string]string)
	for _, path := range paths {
		response := mustGet(t, srv.BaseURL()+path)
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		baseline[path] = string(body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ready, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- store.WithinTx(ctx, func(tx *sqlite.Store) error {
			if err := tx.WipeDerivedResults(ctx, "ev-100", ""); err != nil {
				return err
			}
			race, err := tx.GetRace(ctx, "race-10k")
			if err != nil {
				return err
			}
			race.Name = "Uncommitted replacement"
			if err := tx.UpsertRace(ctx, race); err != nil {
				return err
			}
			close(ready)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("writer did not start")
	}
	defer func() {
		close(release)
		if err := <-done; err != nil {
			t.Errorf("writer completion: %v", err)
		}
	}()
	client := &http.Client{Timeout: time.Second}
	for _, path := range paths {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.BaseURL()+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+testAPIToken)
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("interactive read waits for writer: %s: %v", path, err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK || string(body) != baseline[path] {
			t.Fatalf("view exposes a partial/uncommitted protocol: %s status=%d err=%v", path, response.StatusCode, err)
		}
	}
}
