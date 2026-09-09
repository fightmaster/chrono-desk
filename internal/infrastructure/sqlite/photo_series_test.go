package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

func newPhotoSeriesTestStore(t *testing.T) *Store {
	t.Helper()
	store := newTestStore(t)
	if err := store.UpsertEvent(context.Background(), domain.Event{ID: "event", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	return store
}

type seriesTestFrame struct {
	Timestamp int64  `json:"timestamp_epoch_ms"`
	URL       string `json:"url"`
}

func seriesTestPhoto(t *testing.T, timeMs int64, indexes ...int) Photo {
	t.Helper()
	frames := make([]seriesTestFrame, 0, len(indexes))
	for _, i := range indexes {
		frames = append(frames, seriesTestFrame{timeMs + int64(i)*100, fmt.Sprintf("http://camera/photo?path=photos/event/track/frame_%02d.jpg", i)})
	}
	data, err := json.Marshal(frames)
	if err != nil {
		t.Fatal(err)
	}
	return Photo{ID: "camera:track", SourceID: "camera", TimeMs: timeMs, Frames: data}
}

func assertPhotoSeries(t *testing.T, store *Store, wantCount int) {
	t.Helper()
	photos, err := store.GetPhotosInRange(context.Background(), "event", 0, 100000)
	if err != nil || len(photos) != 1 {
		t.Fatalf("photos=%v err=%v", photos, err)
	}
	if photos[0].TimeMs != 1000 {
		t.Fatalf("first-ingest track time moved: %d", photos[0].TimeMs)
	}
	var frames []seriesTestFrame
	if err := json.Unmarshal(photos[0].Frames, &frames); err != nil {
		t.Fatal(err)
	}
	if len(frames) != wantCount {
		t.Fatalf("frames=%d want=%d: %s", len(frames), wantCount, photos[0].Frames)
	}
	for i, f := range frames {
		if f.Timestamp != 1000+int64(i)*100 {
			t.Errorf("frame %d time=%d: first-ingest calibration not preserved", i, f.Timestamp)
		}
	}
}

func TestPhotoSeriesAppendsWithoutChangingTimes(t *testing.T) {
	for _, firstCount := range []int{0, 1, 3, 4, 8} {
		t.Run(fmt.Sprintf("first_%d", firstCount), func(t *testing.T) {
			store := newPhotoSeriesTestStore(t)
			ctx := context.Background()
			indexes := make([]int, firstCount)
			for i := range indexes {
				indexes[i] = i
			}
			if err := store.UpsertPhoto(ctx, "event", seriesTestPhoto(t, 1000, indexes...), 1); err != nil {
				t.Fatal(err)
			}
			// A later clock estimate must not shift either existing or added frames.
			full := seriesTestPhoto(t, 4000, 7, 6, 5, 4, 3, 2, 1, 0, 3)
			full.Bib, full.BibSource = "123", "ocr"
			for _, p := range []Photo{full, full, seriesTestPhoto(t, 8000, 0, 1), seriesTestPhoto(t, 8000)} {
				if err := store.UpsertPhoto(ctx, "event", p, 2); err != nil {
					t.Fatal(err)
				}
				assertPhotoSeries(t, store, 8)
			}
		})
	}
}

func TestPhotoSeriesConcurrentAppendsAreNotLost(t *testing.T) {
	store := newPhotoSeriesTestStore(t)
	ctx := context.Background()
	if err := store.UpsertPhoto(ctx, "event", seriesTestPhoto(t, 1000, 0), 1); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 7)
	start := make(chan struct{})
	for i := 1; i < 8; i++ {
		p := seriesTestPhoto(t, 3000, i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- store.UpsertPhoto(ctx, "event", p, 2)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertPhotoSeries(t, store, 8)
}

func TestPhotoSeriesMalformedUpdatePreservesStoredData(t *testing.T) {
	store := newPhotoSeriesTestStore(t)
	ctx := context.Background()
	if err := store.UpsertPhoto(ctx, "event", seriesTestPhoto(t, 1000, 0), 1); err != nil {
		t.Fatal(err)
	}
	p := seriesTestPhoto(t, 3000, 1)
	p.Frames = json.RawMessage(`[{`)
	if err := store.UpsertPhoto(ctx, "event", p, 2); err == nil {
		t.Fatal("malformed update must fail without replacing stored data")
	}
	assertPhotoSeries(t, store, 1)
}

func TestPhotoSeriesRefreshesAddressWithoutDuplicatingFrames(t *testing.T) {
	store := newPhotoSeriesTestStore(t)
	ctx := context.Background()
	if err := store.UpsertPhoto(ctx, "event", seriesTestPhoto(t, 1000, 0), 1); err != nil {
		t.Fatal(err)
	}
	p := seriesTestPhoto(t, 4000, 0, 1)
	p.Frames = json.RawMessage(strings.ReplaceAll(string(p.Frames), "http://camera/", "http://new-address:8080/"))
	if err := store.UpsertPhoto(ctx, "event", p, 2); err != nil {
		t.Fatal(err)
	}
	assertPhotoSeries(t, store, 2)
	photos, err := store.GetPhotosInRange(ctx, "event", 0, 100000)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(photos[0].Frames), "http://camera/") {
		t.Fatal("stored frame still uses disconnected address")
	}
}

func TestPhotoSeriesEqualTimesAreDistinctEvidence(t *testing.T) {
	store := newPhotoSeriesTestStore(t)
	ctx := context.Background()
	p := seriesTestPhoto(t, 1000, 0)
	if err := store.UpsertPhoto(ctx, "event", p, 1); err != nil {
		t.Fatal(err)
	}
	p.Frames = json.RawMessage(`[{"timestamp_epoch_ms":1000,"url":"http://camera/photo?path=other.jpg"}]`)
	if err := store.UpsertPhoto(ctx, "event", p, 2); err != nil {
		t.Fatal(err)
	}
	photos, err := store.GetPhotosInRange(ctx, "event", 0, 100000)
	if err != nil {
		t.Fatal(err)
	}
	var frames []seriesTestFrame
	if err := json.Unmarshal(photos[0].Frames, &frames); err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 || frames[0].Timestamp != 1000 || frames[1].Timestamp != 1000 {
		t.Fatalf("same-time evidence was collapsed: %+v", frames)
	}
}

func TestPhotoSeriesCannotMergeIntoAnotherEvent(t *testing.T) {
	store := newPhotoSeriesTestStore(t)
	ctx := context.Background()
	if err := store.UpsertEvent(ctx, domain.Event{ID: "other", Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertPhoto(ctx, "event", seriesTestPhoto(t, 1000, 0), 1); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertPhoto(ctx, "other", seriesTestPhoto(t, 4000, 1), 2); err == nil {
		t.Fatal("same photo id must not modify another event's series")
	}
	assertPhotoSeries(t, store, 1)
}

func TestMergePhotoFramesRejectsMalformedEvidence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		stored   string
		incoming string
	}{
		{"stored JSON", `[{`, `[]`},
		{"incoming object", `[]`, `{}`},
		{"stored missing URL", `[{}]`, `[]`},
		{"incoming missing URL", `[]`, `[{}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := mergePhotoFrames(json.RawMessage(tc.stored), json.RawMessage(tc.incoming), 0); err == nil {
				t.Fatal("malformed evidence accepted")
			}
		})
	}
}

func TestPhotoFrameKeyUsesPathOnlyForCameraEndpoint(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"http://a/photo?path=photos%2Fevent%2Fframe.jpg&w=240", "photo:photos/event/frame.jpg"},
		{"http://b:8080/photo?full=1&path=photos/event/frame.jpg", "photo:photos/event/frame.jpg"},
		{"http://a/image.jpg", "url:http://a/image.jpg"},
		{"http://a/photo", "url:http://a/photo"},
		{"%", "url:%"},
	} {
		if got := photoFrameKey(tc.raw); got != tc.want {
			t.Errorf("key(%q)=%q want=%q", tc.raw, got, tc.want)
		}
	}
}
