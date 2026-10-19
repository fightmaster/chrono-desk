package service

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

type countedPhotoStore struct {
	*sqlite.Store
	scans *atomic.Int64
}

func (s countedPhotoStore) ListPhotosForMerge(ctx context.Context, eventID string) ([]sqlite.Photo, error) {
	s.scans.Add(1)
	return s.Store.ListPhotosForMerge(ctx, eventID)
}

func TestPhotoViewCacheSharesMetadataButAlwaysReadsCurrentFrames(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.UpsertEvent(ctx, domain.Event{ID: "event", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	photo := sqlite.Photo{ID: "p", SourceID: "camera", CameraLabel: "cam", TimeMs: 1000, Bib: "1", Frames: json.RawMessage(`[]`)}
	if err := store.UpsertPhoto(ctx, "event", photo, 1); err != nil {
		t.Fatal(err)
	}
	var scans atomic.Int64
	counted := countedPhotoStore{store, &scans}
	cache := NewPhotoViewCache()
	read := func() []MergedFinish {
		var out []MergedFinish
		err := store.WithinReadSnapshot(ctx, func(tx *sqlite.Store) error {
			var err error
			out, err = cache.Merged(ctx, countedPhotoStore{tx, &scans}, "event", MergeWindowMs, 60)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	first := read()
	for i := 0; i < 10; i++ {
		if err := store.WithinReadSnapshot(ctx, func(tx *sqlite.Store) error {
			p, f, err := cache.Counts(ctx, countedPhotoStore{tx, &scans}, "event")
			if err == nil && (p != 1 || f != 1) {
				t.Errorf("counts=%d/%d", p, f)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(read(), first) {
			t.Fatal("unchanged cached result differs")
		}
	}
	if scans.Load() != 1 {
		t.Fatalf("unchanged list/status rescanned photos %d times", scans.Load())
	}
	photo.Frames = json.RawMessage(`[{"timestamp_epoch_ms":1000,"url":"http://camera/new.jpg"}]`)
	if err := store.UpsertPhoto(ctx, "event", photo, 2); err != nil {
		t.Fatal(err)
	}
	grown := read()
	if scans.Load() != 1 || len(grown) != 1 || string(grown[0].Frames) == string(first[0].Frames) {
		t.Fatal("new frames stale or forced metadata scan")
	}
	photo.Bib = "2"
	if err := store.UpsertPhoto(ctx, "event", photo, 3); err != nil {
		t.Fatal(err)
	}
	if next := read(); scans.Load() != 2 || next[0].Bib != "2" {
		t.Fatal("late bib correction stale")
	}
	// Callers cannot corrupt cache metadata through the returned slice.
	got, err := cache.Merged(ctx, counted, "event", MergeWindowMs, 60)
	if err != nil {
		t.Fatal(err)
	}
	got[0].Bib = "corrupted"
	got[0].Cams[0] = "corrupted"
	if next := read(); next[0].Bib != "2" || next[0].Cams[0] == "corrupted" {
		t.Fatal("response mutated cache")
	}
}

func TestPhotoViewCacheCoalescesConcurrentMissesAndBoundsEntries(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.UpsertEvent(ctx, domain.Event{ID: "event", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	var scans atomic.Int64
	counted := countedPhotoStore{store, &scans}
	cache := NewPhotoViewCache()
	// Independent callers share one immutable version, including an empty event.
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := cache.Counts(ctx, counted, "event"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if scans.Load() != 1 {
		t.Fatalf("concurrent empty polls rescanned %d times", scans.Load())
	}
	for window := int64(1); window <= 10; window++ {
		if _, err := cache.Merged(ctx, counted, "event", window, 60); err != nil {
			t.Fatal(err)
		}
	}
	if len(cache.entries) != 4 {
		t.Fatalf("unbounded cache=%d", len(cache.entries))
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	before := scans.Load()
	if _, _, err := cache.Counts(cancelled, counted, "event"); err == nil || scans.Load() != before {
		t.Fatal("cancelled request started work")
	}
}

func TestPhotoViewCacheKeepsEachCommittedReadSnapshotCoherent(t *testing.T) {
	catalog, err := sqlite.NewEventCatalog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	store, err := catalog.OpenOrCreate("event")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.UpsertEvent(ctx, domain.Event{ID: "event", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	photo := sqlite.Photo{ID: "p", SourceID: "camera", TimeMs: 1000, Bib: "old", Frames: json.RawMessage(`[]`)}
	if err := store.UpsertPhoto(ctx, "event", photo, 1); err != nil {
		t.Fatal(err)
	}
	cache := NewPhotoViewCache()
	readCurrent := func(want string) {
		t.Helper()
		if err := store.WithinReadSnapshot(ctx, func(view *sqlite.Store) error {
			rows, err := cache.Merged(ctx, view, "event", MergeWindowMs, 60)
			if err == nil && (len(rows) != 1 || rows[0].Bib != want) {
				t.Errorf("snapshot bib=%v want=%s", rows, want)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.WithinReadSnapshot(ctx, func(old *sqlite.Store) error {
		if _, err := cache.Merged(ctx, old, "event", MergeWindowMs, 60); err != nil {
			return err
		}
		photo.Bib = "new"
		if err := store.UpsertPhoto(ctx, "event", photo, 2); err != nil {
			return err
		}
		readCurrent("new")
		rows, err := cache.Merged(ctx, old, "event", MergeWindowMs, 60)
		if err == nil && (len(rows) != 1 || rows[0].Bib != "old") {
			t.Errorf("old snapshot mixed a new commit: %+v", rows)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	readCurrent("new")
}

func TestPhotoViewCachePreservesEmptyAndUnlabelledCameraJSON(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.UpsertEvent(ctx, domain.Event{ID: "event", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	cache := NewPhotoViewCache()
	for _, seed := range []bool{false, true} {
		if seed {
			if err := store.UpsertPhoto(ctx, "event", sqlite.Photo{ID: "photo", SourceID: "camera", TimeMs: 1000, Frames: json.RawMessage(`[]`)}, 1); err != nil {
				t.Fatal(err)
			}
		}
		baseline, err := LoadMergedFinishes(ctx, store, "event", MergeWindowMs, 60)
		if err != nil {
			t.Fatal(err)
		}
		cached, err := cache.Merged(ctx, store, "event", MergeWindowMs, 60)
		if err != nil {
			t.Fatal(err)
		}
		left, err := json.Marshal(baseline)
		if err != nil {
			t.Fatal(err)
		}
		right, err := json.Marshal(cached)
		if err != nil {
			t.Fatal(err)
		}
		if string(left) != string(right) {
			t.Fatalf("empty array/camera JSON changed: before=%s after=%s", left, right)
		}
	}
}
