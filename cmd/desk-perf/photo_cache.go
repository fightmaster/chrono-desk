package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/service"
)

// Native fields remain owner-local; no HTTP/listener/image requests are started.
func runLocalPhotoCache(store *sqlite.Store, eventID string) {
	ctx := context.Background()
	facts := func() string { return fingerprint(store.DB(), "SELECT * FROM photos ORDER BY id") }
	before := facts()
	cache := service.NewPhotoViewCache()
	result := map[string]any{"scope": "owner-local sealed copy; metadata and visible frame JSON only; no HTTP, images, ingest or recount", "iterations": 25, "visible_limit": 60}
	var baselineHash string
	for _, cached := range []bool{false, true} {
		phaseSQL := sqlSnapshot()
		start := time.Now()
		hash := sha256.New()
		photos, finishes := 0, 0
		for i := 0; i < 25; i++ {
			var rows []service.MergedFinish
			err := store.WithinReadSnapshot(ctx, func(view *sqlite.Store) error {
				var err error
				if cached {
					photos, finishes, err = cache.Counts(ctx, view, eventID)
					if err == nil {
						rows, err = cache.Merged(ctx, view, eventID, service.MergeWindowMs, 60)
					}
				} else {
					var count int64
					count, err = view.CountPhotos(ctx, eventID)
					photos = int(count)
					if err == nil {
						finishes, err = service.CountMergedFinishes(ctx, view, eventID)
					}
					if err == nil {
						rows, err = service.LoadMergedFinishes(ctx, view, eventID, service.MergeWindowMs, 60)
					}
				}
				return err
			})
			must(err)
			data, err := json.Marshal(map[string]any{"photos": photos, "finishes": finishes, "rows": rows})
			must(err)
			hash.Write(data)
		}
		ms := float64(time.Since(start).Nanoseconds()) / 1e6
		digest := hex.EncodeToString(hash.Sum(nil))
		name := "uncached"
		if cached {
			name = "cached"
			if digest != baselineHash {
				panic("cached photo metadata/count/frame response differs")
			}
		} else {
			baselineHash = digest
		}
		result[name] = map[string]any{"duration_ms": ms, "mean_pair_ms": ms / 25, "sql": sqlSnapshot().since(phaseSQL), "photos": photos, "finishes": finishes, "response_sha256": digest}
	}
	if facts() != before {
		panic("photo comparison modified owner-local facts")
	}
	result["responses_equal"], result["facts_unchanged"] = true, true
	must(json.NewEncoder(os.Stdout).Encode(result))
	fmt.Fprint(os.Stderr, "Photo cache comparison complete; only aggregate counts/hashes emitted.\n")
}
