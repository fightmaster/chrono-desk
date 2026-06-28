package service

import (
	"context"
	"strings"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

func TestAbsoluteURL(t *testing.T) {
	cases := []struct{ base, ref, want string }{
		{"http://10.0.0.5:8080", "/photo?path=a.jpg", "http://10.0.0.5:8080/photo?path=a.jpg"},
		{"http://10.0.0.5:8080/", "/photo?path=a.jpg", "http://10.0.0.5:8080/photo?path=a.jpg"},
		{"http://x", "http://y/z", "http://y/z"},
		{"http://x", "", ""},
	}
	for _, c := range cases {
		if got := absoluteURL(c.base, c.ref); got != c.want {
			t.Errorf("absoluteURL(%q,%q)=%q want %q", c.base, c.ref, got, c.want)
		}
	}
}

func TestToStoredPhotoAppliesSkewAndAbsolutizes(t *testing.T) {
	ev := chronoCamEvent{SourceID: "src", CameraLabel: "Финиш", ServerTimeEpochMs: 5000}
	track := chronoCamTrack{
		ID: "t1", FirstSeenEpochMs: 1000, Bib: "7", BibSource: "ocr",
		BestPhotoURL: "/photo?path=a.jpg",
		Frames:       []chronoCamFrame{{TimestampEpochMs: 500, URL: "/photo?path=b.jpg"}},
	}
	p := toStoredPhoto(ev, "http://10.0.0.5:8080", track, 200)

	if p.ID != "src:t1" {
		t.Errorf("id=%q", p.ID)
	}
	if p.TimeMs != 1200 { // 1000 + skew 200
		t.Errorf("time_ms=%d want 1200", p.TimeMs)
	}
	if p.BestPhotoURL != "http://10.0.0.5:8080/photo?path=a.jpg" {
		t.Errorf("best_photo_url=%q", p.BestPhotoURL)
	}
	frames := string(p.Frames)
	if !strings.Contains(frames, "\"timestamp_epoch_ms\":700") { // 500 + skew
		t.Errorf("frame time not skewed: %s", frames)
	}
	if !strings.Contains(frames, "http://10.0.0.5:8080/photo?path=b.jpg") {
		t.Errorf("frame url not absolute: %s", frames)
	}
}

func TestMatchPhotosOrdersByBibThenTime(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.UpsertEvent(ctx, domain.Event{ID: "e1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	upsert := func(id string, timeMs int64, bib string) {
		if err := store.UpsertPhoto(ctx, "e1", sqlite.Photo{ID: id, TimeMs: timeMs, Bib: bib}, 0); err != nil {
			t.Fatal(err)
		}
	}
	upsert("p1", 1000, "10")
	upsert("p2", 1100, "20")
	upsert("p3", 1200, "10")

	// bib hint "10" wins, then nearest to 1050.
	got, err := MatchPhotos(ctx, store, "e1", 1050, 300, "10")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != "p1" || got[1].ID != "p3" || got[2].ID != "p2" {
		t.Errorf("order = %v", ids(got))
	}

	// Tolerance excludes out-of-window photos.
	got, err = MatchPhotos(ctx, store, "e1", 1050, 60, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 within ±60ms, got %v", ids(got))
	}
}

func TestMatchPhotosFindsBibOutsideToleranceButInWideWindow(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.UpsertEvent(ctx, domain.Event{ID: "e1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	upsert := func(id string, timeMs int64, bib string) {
		if err := store.UpsertPhoto(ctx, "e1", sqlite.Photo{ID: id, TimeMs: timeMs, Bib: bib}, 0); err != nil {
			t.Fatal(err)
		}
	}
	// Runner №10's frame is 500ms off the entered time (outside ±100), another
	// runner is right on it. With the number, №10 must still win.
	upsert("p10", 1500, "10")
	upsert("p20", 1000, "20")

	got, err := MatchPhotos(ctx, store, "e1", 1000, 100, "10")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "p10" {
		t.Errorf("expected bib match p10 first, got %v", ids(got))
	}

	// Without the number, only the in-window runner is returned.
	got, err = MatchPhotos(ctx, store, "e1", 1000, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "p20" {
		t.Errorf("expected only in-window p20, got %v", ids(got))
	}
}

func TestUpsertPhotoFreezesTimeButUpdatesBib(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.UpsertEvent(ctx, domain.Event{ID: "e1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertPhoto(ctx, "e1", sqlite.Photo{ID: "p", TimeMs: 1000}, 0); err != nil {
		t.Fatal(err)
	}
	// A re-poll with a drifted (re-skewed) time must NOT move the stored time, but
	// a newly-recognized number should still land.
	if err := store.UpsertPhoto(ctx, "e1", sqlite.Photo{ID: "p", TimeMs: 5000, Bib: "77", BibSource: "ocr"}, 1); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetPhotosInRange(ctx, "e1", 0, 100000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TimeMs != 1000 {
		t.Errorf("time should be frozen at 1000, got %v", got)
	}
	if got[0].Bib != "77" {
		t.Errorf("bib should update to 77, got %q", got[0].Bib)
	}
}

func TestListRecentPhotosOrdersByTimeDescAndLimits(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.UpsertEvent(ctx, domain.Event{ID: "e1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	for i, tm := range []int64{1000, 3000, 2000} {
		id := string(rune('a' + i))
		if err := store.UpsertPhoto(ctx, "e1", sqlite.Photo{ID: id, TimeMs: tm}, 0); err != nil {
			t.Fatal(err)
		}
	}
	// Newest first.
	got, err := store.ListRecentPhotos(ctx, "e1", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].TimeMs != 3000 || got[1].TimeMs != 2000 || got[2].TimeMs != 1000 {
		t.Errorf("order = %v", ids(got))
	}
	// Limit applies.
	got, err = store.ListRecentPhotos(ctx, "e1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].TimeMs != 3000 {
		t.Errorf("limit not applied: %v", ids(got))
	}
}

func ids(photos []sqlite.Photo) []string {
	out := make([]string, len(photos))
	for i, p := range photos {
		out[i] = p.ID
	}
	return out
}
