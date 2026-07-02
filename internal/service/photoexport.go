package service

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

// BuildMergedFinishesZip produces the coordinated photo-finish export: one row per
// crossing after collapsing the copies several cameras caught (MergeFinishes), plus
// the best JPEG of each bundled INTO the archive so the export stays readable after
// the phones leave the LAN, change IP, or the file is opened on another machine. N
// phones yield one self-contained dataset instead of N separate ZIPs. Returns the
// archive bytes and a file name.
func BuildMergedFinishesZip(ctx context.Context, store *sqlite.Store, cache *PhotoCache, eventID string) ([]byte, string, error) {
	photos, err := store.ListPhotosForMerge(ctx, eventID)
	if err != nil {
		return nil, "", err
	}
	finishes := MergeFinishes(photos, MergeWindowMs)

	// fetch pulls (and caches) a finish's best JPEG. A nil cache — or a phone that has
	// already left the LAN — just yields no image; the row still exports without one.
	fetch := func(rawURL string) ([]byte, error) {
		if cache == nil {
			return nil, fmt.Errorf("нет кэша фото")
		}
		return cache.Get(ctx, rawURL)
	}
	return zipFinishes(finishes, fetch)
}

// zipFinishes is the pure archive builder: it lays out photo-finishes.csv (with a
// relative `photo` column) alongside the bundled JPEGs it could fetch. Split out from
// the DB/cache read so it is unit-tested with a fake fetcher — no store, no network.
func zipFinishes(finishes []MergedFinish, fetch func(rawURL string) ([]byte, error)) ([]byte, string, error) {
	// Pass 1: fetch/cache each best photo, assigning a stable archive path only to the
	// ones we actually have bytes for, so the CSV never points at a missing file.
	photoPath := make([]string, len(finishes))
	for i, f := range finishes {
		if f.BestPhotoURL == "" {
			continue
		}
		if _, err := fetch(f.BestPhotoURL); err != nil {
			continue
		}
		photoPath[i] = fmt.Sprintf("photos/%04d.jpg", i+1)
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	// The CSV, referencing the bundled files by their in-archive path.
	csvEntry, err := zw.Create("photo-finishes.csv")
	if err != nil {
		return nil, "", err
	}
	if _, err := csvEntry.Write([]byte("\xEF\xBB\xBF")); err != nil { // UTF-8 BOM for Excel
		return nil, "", err
	}
	cw := csv.NewWriter(csvEntry)
	_ = cw.Write([]string{
		"time_ms", "time_iso", "bib", "bib_source",
		"cameras", "camera_count", "merged_count", "photo",
	})
	for i, f := range finishes {
		_ = cw.Write([]string{
			strconv.FormatInt(f.TimeMs, 10),
			time.UnixMilli(f.TimeMs).UTC().Format("2006-01-02T15:04:05.000Z07:00"),
			f.Bib,
			f.BibSource,
			strings.Join(f.Cams, " | "),
			strconv.Itoa(len(f.Cams)),
			strconv.Itoa(f.MergedCount),
			photoPath[i],
		})
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return nil, "", err
	}

	// Pass 2: write the bundled JPEGs (each a cheap cache disk-read after pass 1).
	for i, f := range finishes {
		if photoPath[i] == "" {
			continue
		}
		data, err := fetch(f.BestPhotoURL)
		if err != nil {
			continue
		}
		iw, err := zw.Create(photoPath[i])
		if err != nil {
			return nil, "", err
		}
		if _, err := iw.Write(data); err != nil {
			return nil, "", err
		}
	}

	if err := zw.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), "photo-finishes.zip", nil
}

// CountMergedFinishes reports how many distinct crossings the event has after merging
// multi-camera copies — the honest "finishes" total for the wall badge.
func CountMergedFinishes(ctx context.Context, store *sqlite.Store, eventID string) (int, error) {
	photos, err := store.ListPhotosForMerge(ctx, eventID)
	if err != nil {
		return 0, err
	}
	return len(MergeFinishes(photos, MergeWindowMs)), nil
}
