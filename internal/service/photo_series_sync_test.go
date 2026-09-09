package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

func TestPhotoSeriesPollCompletesOlderTrackAfterFullRefreshOrRestart(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprintf("restart_%t", restart), func(t *testing.T) {
			var completed atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/event" {
					// Simulate clock-estimation drift between the partial and full pull.
					clock := time.Now().UnixMilli()
					if completed.Load() {
						clock += 200
					}
					_ = json.NewEncoder(w).Encode(chronoCamEvent{SourceID: "cam", ServerTimeEpochMs: clock})
					return
				}
				if r.URL.Path != "/tracks" {
					http.NotFound(w, r)
					return
				}
				count := 3
				bib := ""
				if completed.Load() {
					count, bib = 8, "123"
				}
				track := chronoCamTrack{ID: "burst", FirstSeenEpochMs: 1000, Bib: bib, BibSource: "ocr"}
				for i := 0; i < count; i++ {
					track.Frames = append(track.Frames, chronoCamFrame{
						TimestampEpochMs: 1000 + int64(i)*100,
						URL:              fmt.Sprintf("/photo?path=photos/event/burst/frame_%02d.jpg", i),
					})
				}
				// A newer track advances the cursor past the unfinished burst.
				tracks := []chronoCamTrack{{ID: "newer", FirstSeenEpochMs: 5000}}
				since, _ := strconv.ParseInt(r.URL.Query().Get("since_ms"), 10, 64)
				if since <= track.FirstSeenEpochMs {
					tracks = append(tracks, track)
				}
				_ = json.NewEncoder(w).Encode(tracks)
			}))
			defer server.Close()

			path := filepath.Join(t.TempDir(), "photos.chrono")
			openStore := func() *sqlite.Store {
				db, err := sqlite.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Close() })
				store, err := sqlite.New(db)
				if err != nil {
					t.Fatal(err)
				}
				return store
			}
			store := openStore()
			ctx := context.Background()
			if err := store.UpsertEvent(ctx, domain.Event{ID: "event", Name: "Test"}); err != nil {
				t.Fatal(err)
			}
			if err := store.UpsertPhotoSource(ctx, "event", sqlite.PhotoSource{BaseURL: server.URL, Enabled: true}); err != nil {
				t.Fatal(err)
			}
			logger := log.New(io.Discard, "", 0)
			manager := NewPhotoManager(logger)
			poll := func(full bool) {
				stats, err := manager.pollOnce(ctx, store, "event", full)
				if err != nil || stats.Errors != 0 {
					t.Fatalf("poll stats=%+v err=%v", stats, err)
				}
			}
			readBurst := func(wantCount int) sqlite.Photo {
				photos, err := store.GetPhotosInRange(ctx, "event", 0, 10000)
				if err != nil {
					t.Fatal(err)
				}
				for _, p := range photos {
					if p.ID != "cam:burst" {
						continue
					}
					var frames []chronoCamFrame
					if err := json.Unmarshal(p.Frames, &frames); err != nil {
						t.Fatal(err)
					}
					if len(frames) != wantCount {
						t.Fatalf("burst frames=%d want=%d", len(frames), wantCount)
					}
					for i, f := range frames {
						if f.TimestampEpochMs != p.TimeMs+int64(i)*100 {
							t.Fatalf("frame %d changed clock reference: %+v track=%d", i, f, p.TimeMs)
						}
					}
					return p
				}
				t.Fatal("burst missing")
				return sqlite.Photo{}
			}
			poll(true)
			before := readBurst(3)
			completed.Store(true)
			poll(false)
			readBurst(3) // since_ms is a creation cursor, not an update cursor.
			if restart {
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				store = openStore()
				manager = NewPhotoManager(logger)
			}
			for i := 0; i < 2; i++ {
				poll(true)
				after := readBurst(8)
				if after.TimeMs != before.TimeMs || after.Bib != "123" {
					t.Fatalf("track time or late OCR incorrect: before=%+v after=%+v", before, after)
				}
				// The merged-wall album reads this same stored frame list.
				frames, err := store.GetFramesByIDs(ctx, "event", []string{after.ID})
				if err != nil || string(frames[after.ID]) != string(after.Frames) {
					t.Fatalf("album series differs: %v", err)
				}
			}
		})
	}
}
