package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

// Chrono Cam live API (the phone serves; the desk pulls). See the phone's
// DATA_FORMAT.md §6. Local-network only.

type chronoCamEvent struct {
	SourceID          string `json:"source_id"`
	CameraLabel       string `json:"camera_label"`
	EventLabel        string `json:"event_label"`
	ServerTimeEpochMs int64  `json:"server_time_epoch_ms"`
}

type chronoCamFrame struct {
	TimestampEpochMs int64  `json:"timestamp_epoch_ms"`
	URL              string `json:"url"`
}

type chronoCamTrack struct {
	ID               string           `json:"id"`
	FirstSeenEpochMs int64            `json:"first_seen_epoch_ms"`
	Status           string           `json:"status"`
	Bib              string           `json:"bib"`
	BibSource        string           `json:"bib_source"`
	BestPhotoURL     string           `json:"best_photo_url"`
	Frames           []chronoCamFrame `json:"frames"`
}

// pullChronoCam fetches /event and /tracks from a phone source.
func pullChronoCam(ctx context.Context, client *http.Client, baseURL string) (chronoCamEvent, []chronoCamTrack, error) {
	var ev chronoCamEvent
	if err := getJSON(ctx, client, absoluteURL(baseURL, "/event"), &ev); err != nil {
		return ev, nil, fmt.Errorf("get /event: %w", err)
	}
	var tracks []chronoCamTrack
	if err := getJSON(ctx, client, absoluteURL(baseURL, "/tracks"), &tracks); err != nil {
		return ev, nil, fmt.Errorf("get /tracks: %w", err)
	}
	return ev, tracks, nil
}

func getJSON(ctx context.Context, client *http.Client, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// absoluteURL resolves a phone-relative path against the source base URL so the
// desk frontend can load images directly from the phone.
func absoluteURL(base, ref string) string {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	if ref == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + ref
}

// toStoredPhoto converts a pulled track into a Photo with desk-clock-corrected
// times (by [skewMs]) and absolute image URLs.
func toStoredPhoto(ev chronoCamEvent, baseURL string, t chronoCamTrack, skewMs int64) sqlite.Photo {
	sourceID := ev.SourceID
	if sourceID == "" {
		sourceID = baseURL
	}
	frames := make([]chronoCamFrame, 0, len(t.Frames))
	for _, f := range t.Frames {
		frames = append(frames, chronoCamFrame{
			TimestampEpochMs: f.TimestampEpochMs + skewMs,
			URL:              absoluteURL(baseURL, f.URL),
		})
	}
	framesJSON, _ := json.Marshal(frames)
	return sqlite.Photo{
		ID:           sourceID + ":" + t.ID,
		SourceID:     sourceID,
		CameraLabel:  ev.CameraLabel,
		TimeMs:       t.FirstSeenEpochMs + skewMs,
		Bib:          t.Bib,
		BibSource:    t.BibSource,
		BestPhotoURL: absoluteURL(baseURL, t.BestPhotoURL),
		Frames:       framesJSON,
	}
}

// MatchPhotos returns finish photos near [timeMs] (within [toleranceMs]), ordered
// best-first: a photo whose bib equals [bibHint] wins, then the closest in time.
// This backs the judge's "show the photo at this fixed time" action.
func MatchPhotos(ctx context.Context, store *sqlite.Store, eventID string, timeMs, toleranceMs int64, bibHint string) ([]sqlite.Photo, error) {
	if toleranceMs < 0 {
		toleranceMs = 0
	}
	photos, err := store.GetPhotosInRange(ctx, eventID, timeMs-toleranceMs, timeMs+toleranceMs)
	if err != nil {
		return nil, err
	}
	bibHint = strings.TrimSpace(bibHint)

	// The number is unique per event, so an exact bib hit IS this runner — find it
	// even when the photo time drifted from the chip/manual time (and to cut
	// through a pack finishing together). Pull exact-bib photos from a wider
	// window and fold them in.
	if bibHint != "" {
		wide := toleranceMs * 4
		if wide < 3000 {
			wide = 3000
		}
		extra, err := store.GetPhotosInRange(ctx, eventID, timeMs-wide, timeMs+wide)
		if err != nil {
			return nil, err
		}
		seen := make(map[string]bool, len(photos))
		for _, p := range photos {
			seen[p.ID] = true
		}
		for _, p := range extra {
			if p.Bib == bibHint && !seen[p.ID] {
				photos = append(photos, p)
				seen[p.ID] = true
			}
		}
	}

	sort.SliceStable(photos, func(i, j int) bool {
		if bibHint != "" {
			mi, mj := photos[i].Bib == bibHint, photos[j].Bib == bibHint
			if mi != mj {
				return mi // exact bib match wins decisively, even if further in time
			}
		}
		return absInt64(photos[i].TimeMs-timeMs) < absInt64(photos[j].TimeMs-timeMs)
	})
	return photos, nil
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
