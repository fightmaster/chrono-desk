package sqlite

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
)

type photoFrame struct {
	TimestampEpochMs int64  `json:"timestamp_epoch_ms"`
	URL              string `json:"url"`
}

// mergePhotoFrames extends evidence, never replaces it with a shorter snapshot.
// Existing times are immutable; new frames use the track's first-ingest offset.
func mergePhotoFrames(stored, incoming json.RawMessage, offsetDelta int64) (json.RawMessage, error) {
	decode := func(data json.RawMessage) ([]photoFrame, error) {
		var frames []photoFrame
		if len(data) != 0 {
			if err := json.Unmarshal(data, &frames); err != nil {
				return nil, fmt.Errorf("invalid frame list: %w", err)
			}
		}
		return frames, nil
	}
	existing, err := decode(stored)
	if err != nil {
		return nil, err
	}
	added, err := decode(incoming)
	if err != nil {
		return nil, err
	}
	frames := make([]photoFrame, 0, len(existing)+len(added))
	seen := make(map[string]int, len(existing)+len(added))
	appendFrames := func(items []photoFrame, delta int64) error {
		for _, frame := range items {
			if frame.URL == "" {
				return fmt.Errorf("frame URL is empty")
			}
			key := photoFrameKey(frame.URL)
			if i, ok := seen[key]; ok {
				// A phone may reconnect on a new IP. Refresh the transport locator,
				// not the time or identity of an already stored evidence frame.
				frames[i].URL = frame.URL
				continue
			}
			frame.TimestampEpochMs += delta
			seen[key] = len(frames)
			frames = append(frames, frame)
		}
		return nil
	}
	if err := appendFrames(existing, 0); err != nil {
		return nil, err
	}
	if err := appendFrames(added, offsetDelta); err != nil {
		return nil, err
	}
	sort.SliceStable(frames, func(i, j int) bool {
		return frames[i].TimestampEpochMs < frames[j].TimestampEpochMs
	})
	return json.Marshal(frames)
}

// The v1 camera contract identifies a JPEG by its immutable app-relative path.
// This key is used only inside one source-qualified track, never across cameras.
func photoFrameKey(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err == nil && u.Path == "/photo" {
		if path := u.Query().Get("path"); path != "" {
			return "photo:" + path
		}
	}
	return "url:" + rawURL
}
