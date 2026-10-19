// desk-perf measures the exact installed service/HTTP paths on a sanitized copy.
// No physical reader, camera or site connection is started.
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"sort"
	"sync"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/processor"
	"gitlab.com/fightmaster1/chrono-desk/internal/service"
	"gitlab.com/fightmaster1/chrono-desk/internal/transport/httpapi"
)

type seedMetadata struct {
	Event     string `json:"event"`
	Member    string `json:"member"`
	Race      string `json:"race"`
	Mode      string `json:"mode"`
	Source    string `json:"sanitized_source_sha256"`
	Scope     string `json:"copy_scope"`
	NativeSHA string `json:"local_database_sha256"`
}
type requestSample struct {
	Path   string  `json:"path"`
	MS     float64 `json:"ms"`
	Status int     `json:"status"`
	Error  bool    `json:"error"`
}
type poolSample struct {
	WaitCount int64   `json:"wait_count"`
	WaitMS    float64 `json:"wait_ms"`
}

func poolDelta(s, before sql.DBStats) poolSample {
	return poolSample{s.WaitCount - before.WaitCount, float64(s.WaitDuration-before.WaitDuration) / 1e6}
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
func get(client *http.Client, base, path string) requestSample {
	start := time.Now()
	r, e := http.NewRequest("GET", base+path, nil)
	must(e)
	r.Header.Set("Authorization", "Bearer isolated-performance-probe")
	response, e := client.Do(r)
	out := requestSample{Path: path}
	if e != nil {
		out.Error = true
	} else {
		out.Status = response.StatusCode
		_, e = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		out.Error = e != nil || response.StatusCode != 200
	}
	out.MS = float64(time.Since(start).Nanoseconds()) / 1e6
	return out
}
func fingerprint(db *sql.DB, query string) string {
	rows, e := db.Query(query)
	must(e)
	defer rows.Close()
	cols, e := rows.Columns()
	must(e)
	h := sha256.New()
	for rows.Next() {
		v := make([]any, len(cols))
		p := make([]any, len(cols))
		for i := range v {
			p[i] = &v[i]
		}
		must(rows.Scan(p...))
		data, e := json.Marshal(v)
		must(e)
		h.Write(data)
		h.Write([]byte{'\n'})
	}
	must(rows.Err())
	return hex.EncodeToString(h.Sum(nil))
}
func protected(db *sql.DB) map[string]string {
	return map[string]string{"raw": fingerprint(db, "SELECT id,event_id,time_ms,epc,number,board,disabled_at FROM rfid_logs ORDER BY id"), "manual": fingerprint(db, "SELECT member_id,race_id,checkpoint_id,time_ms,number FROM results WHERE rfid_log_id IS NULL ORDER BY id"), "judge": fingerprint(db, "SELECT id,status FROM members ORDER BY id")}
}
func main() {
	dir := flag.String("data-dir", "", "Private directory with a preseeded sanitized event database")
	mode := flag.String("mode", "single", "single (legacy pool), separate (prototype) or production (bounded reader pool)")
	cpu := flag.String("cpu-profile", "", "Optional private CPU profile")
	serve := flag.Bool("serve-ui", false, "Serve the API on a query-only private copy for 2 minutes; no benchmark replay or ingest")
	parity := flag.Bool("local-parity", false, "Recount only an owner-local sealed copy and emit hashes/counts, never serve HTTP or export personal fields")
	photoCompare := flag.Bool("local-photo-cache", false, "Compare old and cached photo views on an owner-local sealed copy; only aggregate statistics leave the process")
	flag.Parse()
	if *photoCompare && (*parity || *serve) {
		panic("photo comparison cannot serve HTTP or recount")
	}
	if *dir == "" || (*mode != "single" && *mode != "separate" && *mode != "production") {
		panic("explicit private copy and mode required")
	}
	abs, e := filepath.Abs(*dir)
	must(e)
	info, e := os.Stat(abs)
	must(e)
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		panic("private0700 copy directory required")
	}
	data, e := os.ReadFile(filepath.Join(abs, "seed-metadata.json"))
	must(e)
	var seed seedMetadata
	must(json.Unmarshal(data, &seed))
	if (seed.Source == "" && seed.NativeSHA == "") || seed.Mode != *mode || seed.Event == "" {
		panic("sanitized seed metadata/mode required")
	}
	if seed.Scope == "owner-mac-local-only" {
		if (!*parity && !*photoCompare) || *mode != "single" || seed.NativeSHA == "" || *serve {
			panic("native owner-local copies require single-mode local-only comparison")
		}
		sealed, e := os.ReadFile(filepath.Join(abs, seed.Event+".chrono"))
		must(e)
		digest := sha256.Sum256(sealed)
		if hex.EncodeToString(digest[:]) != seed.NativeSHA {
			panic("native copy differs from its closed-copy seal")
		}
	} else if seed.NativeSHA != "" {
		panic("unrecognized native copy scope")
	}
	if _, e = os.Stat(filepath.Join(abs, "probe-result.json")); e == nil {
		panic("preserve prior probe; use another fresh copy")
	}
	eventID := seed.Event
	logger := log.New(io.Discard, "", 0)
	readerCount := 0
	if *mode == "production" {
		readerCount = 4
	}
	catalog, e := sqlite.NewEventCatalog(abs, sqlite.WithReadConnections(readerCount))
	must(e)
	events := service.NewEventService(catalog, logger)
	must(e)
	defer events.Close()
	store, e := events.Open(eventID)
	must(e)
	if *photoCompare {
		runLocalPhotoCache(store, eventID)
		return
	}
	if *parity {
		runLocalParity(store, eventID, logger)
		return
	}
	uiEvents := events
	if *mode == "separate" {
		readCatalog, err := sqlite.NewEventCatalog(abs, sqlite.WithReadConnections(0))
		must(err)
		uiEvents = service.NewEventService(readCatalog, logger)
		must(e)
		defer uiEvents.Close()
		read, e := uiEvents.Open(eventID)
		must(e)
		_, e = read.DB().Exec("PRAGMA query_only=ON")
		must(e)
	}
	uiStore, e := uiEvents.Open(eventID)
	must(e)
	server, e := httpapi.New("127.0.0.1:0", uiEvents, service.NewLiveManager(logger), service.NewPhotoManager(logger), nil, nil, nil, logger, "isolated-performance-probe")
	must(e)
	go func() {
		if e := server.Start(); e != nil && !errors.Is(e, http.ErrServerClosed) {
			panic(e)
		}
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		must(server.Shutdown(ctx))
	}()
	if *serve {
		_, e = uiStore.DB().Exec("PRAGMA query_only=ON")
		must(e)
		must(json.NewEncoder(os.Stdout).Encode(map[string]string{"base": server.BaseURL(), "event": seed.Event, "member": seed.Member, "race": seed.Race}))
		time.Sleep(2 * time.Minute)
		return
	}
	if *cpu != "" {
		f, e := os.Create(*cpu)
		must(e)
		defer f.Close()
		must(pprof.StartCPUProfile(f))
		defer pprof.StopCPUProfile()
	}
	client := &http.Client{Timeout: 120 * time.Second}
	base := server.BaseURL()
	paths := []string{"/api/events/" + eventID + "/live/feed?limit=50", "/api/events/" + eventID + "/members/" + seed.Member + "/passes", "/api/events/" + eventID + "/races/" + seed.Race + "/protocol"}
	result := map[string]any{"mode": *mode, "source_sha256": seed.Source, "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH, "baseline_revision": "f8ddc425713fa693b14cbc6e8dec6bbd2383267d", "scope": "Isolated local HTTP/storage reproduction; no WebView render or physical-reader network trace"}
	if build, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range build.Settings {
			if setting.Key == "vcs.revision" || setting.Key == "vcs.modified" {
				result[setting.Key] = setting.Value
			}
		}
	}
	original := protected(store.DB())
	idleSQL := sqlSnapshot()
	idlePool := uiStore.ReadStats()
	idle := []requestSample{}
	for n := 0; n < 15; n++ {
		for _, p := range paths {
			idle = append(idle, get(client, base, p))
		}
	}
	result["idle"] = map[string]any{"requests": idle, "sql": sqlSnapshot().since(idleSQL), "ui_pool": poolDelta(uiStore.ReadStats(), idlePool)}
	// Reproduce a real full recount holding its actual transaction, with parallel
	// HTTP reads and one newly arriving tag waiting for the same writer handle.
	var duplicate domain.RfidLog
	must(store.DB().QueryRow("SELECT id,status,number,time_ms,ant,epc,rssi,board FROM rfid_logs WHERE event_id=? AND disabled_at IS NULL ORDER BY time_ms LIMIT 1", eventID).Scan(&duplicate.ID, &duplicate.Status, &duplicate.Number, &duplicate.TimeMs, &duplicate.Ant, &duplicate.EPC, &duplicate.RSSI, &duplicate.Board))
	duplicate.EventID = eventID
	duplicate.CaptureSourceID = "chrono-desk:" + eventID + ":" + duplicate.Board
	initialSQL := sqlSnapshot()
	initialUI := uiStore.ReadStats()
	initialWriter := store.DB().Stats()
	started := time.Now()
	done := make(chan struct{})
	var recount service.RecountStats
	var recountErr error
	var recountMS float64
	go func() {
		defer close(done)
		recount, recountErr = service.NewRecounter(store, logger, false).Recount(context.Background(), eventID, "")
		recountMS = float64(time.Since(started).Nanoseconds()) / 1e6
	}()
	deadline := time.Now().Add(10 * time.Second)
	for store.DB().Stats().InUse == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	samples := []requestSample{}
	var mu sync.Mutex
	var workers sync.WaitGroup
	var duplicateWaitMS float64
	var duplicateErr error
	workers.Add(1)
	go func() {
		defer workers.Done()
		start := time.Now()
		n, e := store.InsertOwnedRfidLogs(context.Background(), []domain.RfidLog{duplicate})
		duplicateWaitMS = float64(time.Since(start).Nanoseconds()) / 1e6
		duplicateErr = e
		if e == nil && n != 0 {
			duplicateErr = fmt.Errorf("existing replay input acquired duplicate row")
		}
	}()
	for _, p := range paths {
		workers.Add(1)
		go func(p string) {
			defer workers.Done()
			for {
				s := get(client, base, p)
				mu.Lock()
				samples = append(samples, s)
				mu.Unlock()
				select {
				case <-done:
					return
				case <-time.After(100 * time.Millisecond):
				}
			}
		}(p)
	}
	<-done
	workers.Wait()
	must(recountErr)
	must(duplicateErr)
	result["recount"] = map[string]any{"duration_ms": recountMS, "duplicate_ingest_wait_ms": duplicateWaitMS, "replayed": recount, "requests": samples, "sql": sqlSnapshot().since(initialSQL), "ui_pool": poolDelta(uiStore.ReadStats(), initialUI), "writer_pool": poolDelta(store.DB().Stats(), initialWriter)}
	// Raw and judge facts must survive the diagnostic replay. Derived history in
	// this isolated copy may change; no application database is accepted as input.
	final := protected(store.DB())
	for k, v := range original {
		if final[k] != v {
			panic("protected copy fact changed: " + k)
		}
	}
	result["derived_after_recount"] = map[string]string{
		"passes":       fingerprint(store.DB(), "SELECT member_id,race_id,checkpoint_id,time_ms,rfid_log_id FROM results ORDER BY member_id,race_id,checkpoint_id,time_ms,rfid_log_id"),
		"member_times": fingerprint(store.DB(), "SELECT id,start_time_ms,start_time_source,start_observation_id,finish_time_ms,clean_time FROM members ORDER BY id"),
	}
	logs, e := store.ListRfidLogs(context.Background(), eventID)
	must(e)
	active := []domain.RfidLog{}
	for _, r := range logs {
		if r.DisabledAt == nil {
			active = append(active, r)
		}
	}
	if len(active) == 0 {
		panic("active reads required")
	}
	proc := processor.New(sqlite.NewProcessorRepo(store), logger, false)
	ingestSQL := sqlSnapshot()
	ingestPool := uiStore.ReadStats()
	ingestStart := time.Now()
	ingestDone := make(chan struct{})
	var ingestErr error
	var inserted int64
	ingestLatencies := []float64{}
	go func() {
		defer close(ingestDone)
		for i := 0; i < 200; i++ {
			r := active[i%len(active)]
			r.ID = fmt.Sprintf("probe-new-%06d", i)
			r.CaptureSourceID = "chrono-desk:" + eventID + ":" + r.Board
			r.TimeMs += int64(i + 1)
			s := time.Now()
			n, e := store.InsertOwnedRfidLogs(context.Background(), []domain.RfidLog{r})
			if e == nil && n > 0 {
				inserted += n
				e = proc.Process(context.Background(), r, "")
			}
			if e != nil {
				ingestErr = e
				return
			}
			ingestLatencies = append(ingestLatencies, float64(time.Since(s).Nanoseconds())/1e6)
			target := ingestStart.Add(time.Duration(i+1) * 10 * time.Millisecond)
			if delay := time.Until(target); delay > 0 {
				time.Sleep(delay)
			}
		}
	}()
	ingestReads := []requestSample{}
	for {
		for _, p := range paths {
			ingestReads = append(ingestReads, get(client, base, p))
		}
		select {
		case <-ingestDone:
			goto finished
		case <-time.After(50 * time.Millisecond):
		}
	}
finished:
	must(ingestErr)
	result["ingest"] = map[string]any{"inserted": inserted, "requested_rate_per_second": 100, "duration_ms": float64(time.Since(ingestStart).Nanoseconds()) / 1e6, "mark_processing_ms": ingestLatencies, "requests": ingestReads, "sql": sqlSnapshot().since(ingestSQL), "ui_pool": poolDelta(uiStore.ReadStats(), ingestPool)}
	// A no-op evidence check is run separately because the real five-second pull
	// invokes it even when no replay is selected; never contact the real website.
	evidenceSQL := sqlSnapshot()
	evidenceStart := time.Now()
	_, e = store.ProjectionFenceEvidence(context.Background(), eventID)
	must(e)
	result["evidence_check"] = map[string]any{"duration_ms": float64(time.Since(evidenceStart).Nanoseconds()) / 1e6, "sql": sqlSnapshot().since(evidenceSQL)}
	result["protected_before_ingest_unchanged"] = true
	result["photo_sources"] = 0
	raw, e := json.MarshalIndent(result, "", "  ")
	must(e)
	must(os.WriteFile(filepath.Join(abs, "probe-result.json"), append(raw, '\n'), 0o600))
	summary := map[string]any{"mode": *mode, "recount_ms": recountMS, "duplicate_ingest_wait_ms": duplicateWaitMS, "ingest_inserted": inserted, "protected_unchanged": true}
	for _, phase := range []string{"idle", "recount", "ingest"} {
		p := result[phase].(map[string]any)
		reqs := p["requests"].([]requestSample)
		v := []float64{}
		errs := 0
		for _, r := range reqs {
			v = append(v, r.MS)
			if r.Error {
				errs++
			}
		}
		sort.Float64s(v)
		if len(v) > 0 {
			summary[phase] = map[string]any{"samples": len(v), "p50_ms": v[len(v)/2], "p95_ms": v[int(float64(len(v)-1)*.95)], "p99_ms": v[int(float64(len(v)-1)*.99)], "max_ms": v[len(v)-1], "errors": errs, "sql": p["sql"], "ui_pool": p["ui_pool"]}
		}
	}
	out, e := json.Marshal(summary)
	must(e)
	fmt.Println(string(out))
}

// This mode never creates an HTTP server, ingest, photo or relay manager.
// Names/DOB remain on the owner's machine and enter only an aggregate hash.
func runLocalParity(store *sqlite.Store, eventID string, logger *log.Logger) {
	ctx := context.Background()
	facts := func() map[string]string {
		return map[string]string{
			"raw":          fingerprint(store.DB(), "SELECT * FROM rfid_logs ORDER BY id"),
			"manual":       fingerprint(store.DB(), "SELECT * FROM results WHERE rfid_log_id IS NULL ORDER BY id"),
			"registration": fingerprint(store.DB(), "SELECT id,event_id,race_id,category_id,number,epc,rfid,first_name,last_name,gender,dob,city,team,status FROM members ORDER BY id"),
		}
	}
	before := facts()
	start := time.Now()
	stats, err := service.NewRecounter(store, logger, false).Recount(ctx, eventID, "")
	must(err)
	ms := float64(time.Since(start).Nanoseconds()) / 1e6
	after := facts()
	for key, value := range before {
		if after[key] != value {
			panic("owner-local protected fact changed: " + key)
		}
	}
	races, err := store.ListRaces(ctx, eventID)
	must(err)
	protocol := sha256.New()
	rows, finished := 0, 0
	for _, race := range races {
		view, err := service.BuildProtocol(ctx, store, race.ID)
		must(err)
		data, err := json.Marshal(view)
		must(err)
		protocol.Write(data)
		protocol.Write([]byte{'\n'})
		rows += len(view.Rows)
		finished += view.Counts.Finished
	}
	must(json.NewEncoder(os.Stdout).Encode(map[string]any{
		"scope":      "owner-local copy; no personal fields exported; no HTTP or ingest started",
		"recount_ms": ms, "stats": stats, "races": len(races), "rows": rows, "finished": finished,
		"protected_unchanged": true, "protected_hashes": after,
		"protocol_sha256":     hex.EncodeToString(protocol.Sum(nil)),
		"passes_sha256":       fingerprint(store.DB(), "SELECT member_id,race_id,checkpoint_id,rfid_log_id,time_ms,number FROM results ORDER BY member_id,race_id,time_ms,rfid_log_id"),
		"member_times_sha256": fingerprint(store.DB(), "SELECT id,start_time_ms,start_time_source,start_observation_id,finish_time_ms,clean_time FROM members ORDER BY id"),
	}))
}
