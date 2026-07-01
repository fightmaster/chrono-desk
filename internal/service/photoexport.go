package service

import (
	"bytes"
	"context"
	"encoding/csv"
	"strconv"
	"strings"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

// BuildMergedFinishesCSV produces the coordinated photo-finish export: one row per
// crossing after collapsing the copies seen by several cameras (MergeFinishes), so N
// phones yield one combined dataset instead of N separate ZIPs. Metadata only — the
// image bytes stay reachable via best_photo_url. Returns the CSV and a file name.
func BuildMergedFinishesCSV(ctx context.Context, store *sqlite.Store, eventID string) ([]byte, string, error) {
	photos, err := store.ListPhotosForMerge(ctx, eventID)
	if err != nil {
		return nil, "", err
	}
	finishes := MergeFinishes(photos, MergeWindowMs)

	var buf bytes.Buffer
	buf.WriteString("\xEF\xBB\xBF") // UTF-8 BOM so Excel reads Cyrillic camera labels
	cw := csv.NewWriter(&buf)
	_ = cw.Write([]string{
		"time_ms", "time_iso", "bib", "bib_source",
		"cameras", "camera_count", "merged_count", "best_photo_url",
	})
	for _, f := range finishes {
		_ = cw.Write([]string{
			strconv.FormatInt(f.TimeMs, 10),
			time.UnixMilli(f.TimeMs).UTC().Format("2006-01-02T15:04:05.000Z07:00"),
			f.Bib,
			f.BibSource,
			strings.Join(f.Cams, " | "),
			strconv.Itoa(len(f.Cams)),
			strconv.Itoa(f.MergedCount),
			f.BestPhotoURL,
		})
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), "photo-finishes.csv", nil
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
