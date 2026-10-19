package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/service"
)

// Leaving Live cancels display GETs, not the app-owned TCP listener or camera
// poller. Exercise a hidden-view interval with real local transports; elapsed
// five-minute browser idleness is not necessary to establish this lifecycle.
func TestBackgroundIngestPersistsWithoutLiveDisplayRequests(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var initial int
	if err := store.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM rfid_logs").Scan(&initial); err != nil {
		t.Fatal(err)
	}
	var available, imageDownloads atomic.Int64
	camera := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/event":
			_ = json.NewEncoder(w).Encode(map[string]any{"source_id": "camera", "server_time_epoch_ms": time.Now().UnixMilli()})
		case "/tracks":
			tracks := make([]map[string]any, 0, available.Load())
			for i := int64(0); i < available.Load(); i++ {
				tracks = append(tracks, map[string]any{"id": fmt.Sprint(i), "first_seen_epoch_ms": 1780813200000 + i*1000, "best_photo_url": fmt.Sprintf("/photo?id=%d", i), "frames": []map[string]any{{"timestamp_epoch_ms": 1780813200000 + i*1000, "url": fmt.Sprintf("/photo?id=%d", i)}}})
			}
			_ = json.NewEncoder(w).Encode(tracks)
		case "/photo":
			imageDownloads.Add(1)
			_, _ = w.Write([]byte("image"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer camera.Close()
	if err := store.UpsertPhotoSource(ctx, "ev-100", sqlite.PhotoSource{BaseURL: camera.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	srv.photos.Start(store, "ev-100")
	port := freePort(t)
	var status service.LiveStatus
	decodeBody(t, mustPost(t, srv.BaseURL()+"/api/events/ev-100/live/start", "application/json", strings.NewReader(fmt.Sprintf(`{"port":"%d"}`, port))), &status)
	// Abort a display request as the UI does on destroy. No stop command follows.
	requestCtx, stopDisplay := context.WithCancel(ctx)
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, srv.BaseURL()+"/api/events/ev-100/live/feed?limit=40", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testAPIToken)
	stopDisplay()
	if response, err := http.DefaultClient.Do(request); err == nil {
		response.Body.Close()
		t.Fatal("cancelled display request completed")
	}
	available.Store(120)
	var reader net.Conn
	for ctx.Err() == nil {
		reader, err = net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	_ = reader.SetDeadline(time.Now().Add(10 * time.Second))
	acks := bufio.NewReader(reader)
	for i := 0; i < 100; i++ {
		stamp := time.Date(2026, 6, 7, 6, 20, 0, 0, time.UTC).Add(time.Duration(i) * time.Millisecond).Format(time.RFC3339Nano)
		frame := fmt.Sprintf(`{"DeviceCode":"U659","time":%q,"epc":"E280AAA","bat":90,"rssi":-55,"channelId":1}`, stamp)
		if _, err := reader.Write([]byte(frame)); err != nil {
			t.Fatal(err)
		}
		if ack, err := acks.ReadString('\n'); err != nil || ack != "ok\n" {
			t.Fatalf("read %d ack=%q err=%v", i, ack, err)
		}
	}
	// Inspect persistence without any display endpoint driving photo or RFID work.
	for {
		count, err := store.CountPhotos(ctx, "ev-100")
		if err != nil {
			t.Fatal(err)
		}
		if count == 120 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("background camera polling stopped with display")
		case <-time.After(20 * time.Millisecond):
		}
	}
	var stored int
	if err := store.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM rfid_logs").Scan(&stored); err != nil || stored != initial+100 {
		t.Fatalf("hidden-view reads persisted=%d initial=%d err=%v", stored, initial, err)
	}
	if !srv.live.Status("ev-100").AnyRunning || !srv.photos.Running("ev-100") {
		t.Fatal("leaving display stopped an ingest service")
	}
	var feed []sqlite.LivePass
	decodeBody(t, mustGet(t, srv.BaseURL()+"/api/events/ev-100/live/feed?limit=40"), &feed)
	if len(feed) != 40 {
		t.Fatalf("returning view fetched accumulated RFID history: %d", len(feed))
	}
	var photos []sqlite.Photo
	decodeBody(t, mustGet(t, srv.BaseURL()+"/api/events/ev-100/photos/recent?limit=60"), &photos)
	if len(photos) != 60 {
		t.Fatalf("returning view fetched accumulated photos: %d", len(photos))
	}
	if imageDownloads.Load() != 0 {
		t.Fatal("background metadata/display listing downloaded image bytes")
	}
}
