package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// Finish photos pulled from Chrono Cam phones (see schema.sql). Read-only metadata
// the judge matches to a fixed time; never touches the timing path.

// PhotoSource is a registered phone to poll.
type PhotoSource struct {
	BaseURL     string `json:"base_url"`
	SourceID    string `json:"source_id"`
	CameraLabel string `json:"camera_label"`
	// SkewMs is an OPTIONAL manual calibration offset (ms) added to the phone's
	// own crossing time — default 0. It is NOT derived from network latency; the
	// finish time comes from the phone and is independent of transfer timing.
	SkewMs     int64 `json:"skew_ms"`
	LastSeenAt int64 `json:"last_seen_at"`
	Enabled    bool  `json:"enabled"`
}

// Photo is one finish track pulled from a phone, time-corrected to the desk clock.
type Photo struct {
	ID           string          `json:"id"`
	SourceID     string          `json:"source_id"`
	CameraLabel  string          `json:"camera_label"`
	TimeMs       int64           `json:"time_ms"`
	Bib          string          `json:"bib"`
	BibSource    string          `json:"bib_source"`
	BestPhotoURL string          `json:"best_photo_url"`
	Frames       json.RawMessage `json:"frames"`
}

// UpsertPhotoSource registers/updates a source the operator wants polled.
func (s *Store) UpsertPhotoSource(ctx context.Context, eventID string, src PhotoSource) error {
	enabled := 0
	if src.Enabled {
		enabled = 1
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO photo_sources (base_url, event_id, source_id, camera_label, skew_ms, last_seen_at, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(base_url) DO UPDATE SET
			source_id=excluded.source_id, camera_label=excluded.camera_label,
			skew_ms=excluded.skew_ms, last_seen_at=excluded.last_seen_at, enabled=excluded.enabled`,
		src.BaseURL, eventID, src.SourceID, src.CameraLabel, src.SkewMs, nullableMs(src.LastSeenAt), enabled)
	if err != nil {
		return fmt.Errorf("upsert photo source %s: %w", src.BaseURL, err)
	}
	return nil
}

// ListPhotoSources returns the event's registered sources.
func (s *Store) ListPhotoSources(ctx context.Context, eventID string) ([]PhotoSource, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT base_url, source_id, camera_label, skew_ms, COALESCE(last_seen_at, 0), enabled
		FROM photo_sources WHERE event_id = ? ORDER BY base_url`, eventID)
	if err != nil {
		return nil, fmt.Errorf("list photo sources: %w", err)
	}
	defer rows.Close()

	sources := make([]PhotoSource, 0)
	for rows.Next() {
		var src PhotoSource
		var enabled int
		if err := rows.Scan(&src.BaseURL, &src.SourceID, &src.CameraLabel, &src.SkewMs, &src.LastSeenAt, &enabled); err != nil {
			return nil, err
		}
		src.Enabled = enabled != 0
		sources = append(sources, src)
	}
	return sources, rows.Err()
}

// DeletePhotoSource unregisters a source (its photos are left in place).
func (s *Store) DeletePhotoSource(ctx context.Context, eventID, baseURL string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM photo_sources WHERE event_id = ? AND base_url = ?`, eventID, baseURL)
	if err != nil {
		return fmt.Errorf("delete photo source: %w", err)
	}
	return nil
}

// UpsertPhoto stores/refreshes a pulled photo idempotently by id.
func (s *Store) UpsertPhoto(ctx context.Context, eventID string, p Photo, fetchedAt int64) error {
	frames := string(p.Frames)
	if frames == "" {
		frames = "[]"
	}
	// time_ms and frames_json are FROZEN at first ingest: re-polling recomputes the
	// clock skew (which jitters with network latency on a slow LAN), and rewriting
	// the time would make a finish photo drift out from under a capture/manual
	// finish created from its earlier value. Only the mutable display fields update.
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO photos (id, event_id, source_id, camera_label, time_ms, bib, bib_source, best_photo_url, frames_json, fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			camera_label=excluded.camera_label, bib=excluded.bib,
			bib_source=excluded.bib_source, best_photo_url=excluded.best_photo_url,
			fetched_at=excluded.fetched_at`,
		p.ID, eventID, p.SourceID, p.CameraLabel, p.TimeMs, p.Bib, p.BibSource, p.BestPhotoURL, frames, fetchedAt)
	if err != nil {
		return fmt.Errorf("upsert photo %s: %w", p.ID, err)
	}
	return nil
}

// GetPhotosInRange returns photos whose time falls within [startMs, endMs],
// closest to the centre first when paired with a target via the service layer.
func (s *Store) GetPhotosInRange(ctx context.Context, eventID string, startMs, endMs int64) ([]Photo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, source_id, camera_label, time_ms, bib, bib_source, best_photo_url, frames_json
		FROM photos WHERE event_id = ? AND time_ms BETWEEN ? AND ? ORDER BY time_ms`,
		eventID, startMs, endMs)
	if err != nil {
		return nil, fmt.Errorf("get photos in range: %w", err)
	}
	return scanPhotos(rows)
}

// ListRecentPhotos returns the newest photos by finish time (desc) — backs the
// optional "live wall" of finishes as they arrive.
func (s *Store) ListRecentPhotos(ctx context.Context, eventID string, limit int) ([]Photo, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, source_id, camera_label, time_ms, bib, bib_source, best_photo_url, frames_json
		FROM photos WHERE event_id = ? ORDER BY time_ms DESC LIMIT ?`,
		eventID, limit)
	if err != nil {
		return nil, fmt.Errorf("list recent photos: %w", err)
	}
	return scanPhotos(rows)
}

// ListPhotosForMerge returns the event's photos with only the fields MergeFinishes
// needs (no frames_json) — cheap enough to run on every status poll and for the
// combined-finishes export.
func (s *Store) ListPhotosForMerge(ctx context.Context, eventID string) ([]Photo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, source_id, camera_label, time_ms, bib, bib_source, best_photo_url
		FROM photos WHERE event_id = ? ORDER BY time_ms DESC`, eventID)
	if err != nil {
		return nil, fmt.Errorf("list photos for merge: %w", err)
	}
	defer rows.Close()
	photos := make([]Photo, 0)
	for rows.Next() {
		var p Photo
		if err := rows.Scan(&p.ID, &p.SourceID, &p.CameraLabel, &p.TimeMs, &p.Bib, &p.BibSource, &p.BestPhotoURL); err != nil {
			return nil, err
		}
		photos = append(photos, p)
	}
	return photos, rows.Err()
}

// CountPhotos reports how many photos are stored for an event.
func (s *Store) CountPhotos(ctx context.Context, eventID string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM photos WHERE event_id = ?`, eventID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count photos: %w", err)
	}
	return n, nil
}

func scanPhotos(rows *sql.Rows) ([]Photo, error) {
	defer rows.Close()
	photos := make([]Photo, 0)
	for rows.Next() {
		var p Photo
		var frames string
		if err := rows.Scan(&p.ID, &p.SourceID, &p.CameraLabel, &p.TimeMs, &p.Bib, &p.BibSource, &p.BestPhotoURL, &frames); err != nil {
			return nil, err
		}
		p.Frames = json.RawMessage(frames)
		photos = append(photos, p)
	}
	return photos, rows.Err()
}

func nullableMs(ms int64) any {
	if ms <= 0 {
		return nil
	}
	return ms
}
