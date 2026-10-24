package publicweb

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type loadFinishClient struct {
	cancel     context.CancelFunc
	response   *http.Response
	done       chan struct{}
	errors     chan error
	snapshots  atomic.Uint64
	lastNumber atomic.Int64
}

func openLoadFinishClient(t *testing.T, url string, drain bool) *loadFinishClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		cancel()
		res.Body.Close()
		t.Fatalf("load client HTTP%d", res.StatusCode)
	}
	c := &loadFinishClient{cancel: cancel, response: res, done: make(chan struct{}), errors: make(chan error, 1)}
	if !drain {
		close(c.done)
		return c
	}
	go func() {
		defer close(c.done)
		scanner := bufio.NewScanner(res.Body)
		scanner.Buffer(nil, maxFinishFrameBytes+1024)
		var kind string
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				kind = strings.TrimPrefix(line, "event: ")
			}
			if kind != "snapshot" || !strings.HasPrefix(line, "data: ") {
				continue
			}
			var data publicFinishes
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &data); err != nil {
				c.errors <- err
				return
			}
			if len(data.Rows) > 10 {
				c.errors <- fmt.Errorf("received %d rows", len(data.Rows))
				return
			}
			keys := map[string]bool{}
			for _, row := range data.Rows {
				if keys[row.Key] {
					c.errors <- fmt.Errorf("duplicate participation")
					return
				}
				keys[row.Key] = true
			}
			if len(data.Rows) > 0 && data.Rows[0].Number != nil {
				c.lastNumber.Store(*data.Rows[0].Number)
			}
			c.snapshots.Add(1)
		}
		if ctx.Err() == nil {
			c.errors <- fmt.Errorf("unexpected stream termination: %v", scanner.Err())
		}
	}()
	return c
}

func (c *loadFinishClient) close() { c.cancel(); c.response.Body.Close(); <-c.done }

func percentile95(values []time.Duration) time.Duration {
	copy := append([]time.Duration(nil), values...)
	sort.Slice(copy, func(i, j int) bool { return copy[i] < copy[j] })
	return copy[(len(copy)*95+99)/100-1]
}

// Explicit opt-in: ordinary CI keeps deterministic backpressure/lifecycle tests.
// This run measures three matched repetitions before any long-running soak.
func TestFinishStreamMatchedIngestPerformance(t *testing.T) {
	if os.Getenv("CHRONO_PRESENTER_PERF") == "" {
		t.Skip("set CHRONO_PRESENTER_PERF=1 for matched ingestion measurements")
	}
	var off, on []time.Duration
	for _, enabled := range []bool{false, true} {
		for repetition := 0; repetition < 3; repetition++ {
			t.Run(fmt.Sprintf("stream_%t_repeat_%d", enabled, repetition), func(t *testing.T) {
				f := newLiveFinishFixture(t, 1000)
				if enabled {
					ts := httptest.NewServer(f.server.routes())
					defer ts.Close()
					a := openLoadFinishClient(t, ts.URL+"/api/live/finishes/stream", true)
					defer a.close()
					b := openLoadFinishClient(t, ts.URL+"/api/live/finishes/stream", true)
					defer b.close()
					slow := openLoadFinishClient(t, ts.URL+"/api/live/finishes/stream", false)
					defer slow.close()
				}
				// Warm input/driver paths independently of the measured samples.
				for i := 0; i < 30; i++ {
					p, ack := f.frame(0, false)
					if err := f.exchange(p, ack); err != nil {
						t.Fatal(err)
					}
				}
				samples := make([]time.Duration, 0, 200)
				tick := time.NewTicker(10 * time.Millisecond)
				defer tick.Stop()
				for i := 0; i < 200; i++ {
					<-tick.C
					member := 0
					if i%10 == 0 {
						member = i/10 + 1
					}
					p, ack := f.frame(member, i%2 == 0)
					start := time.Now()
					if err := f.exchange(p, ack); err != nil {
						t.Fatal(err)
					}
					samples = append(samples, time.Since(start))
				}
				p95 := percentile95(samples)
				if enabled {
					on = append(on, p95)
				} else {
					off = append(off, p95)
				}
				t.Logf("samples=%d ACK p95=%s", len(samples), p95)
			})
		}
	}
	sort.Slice(off, func(i, j int) bool { return off[i] < off[j] })
	sort.Slice(on, func(i, j int) bool { return on[i] < on[j] })
	// Freeze the noise rule in advance: observed baseline range or a250us
	// scheduling floor, whichever is greater, plus the requested10% budget.
	noise := off[2] - off[0]
	if noise < 250*time.Microsecond {
		noise = 250 * time.Microsecond
	}
	budget := off[1] + off[1]/10 + noise
	t.Logf("baseline_median_p95=%s stream_median_p95=%s baseline_noise=%s budget=%s", off[1], on[1], noise, budget)
	if on[1] > budget {
		t.Fatalf("ingestion p95 regressed beyond measured-noise/10%% budget")
	}
}

// A short duration rehearses the harness only. Release evidence requires30m.
func TestFinishStreamSoak(t *testing.T) {
	value := os.Getenv("CHRONO_PRESENTER_SOAK")
	if value == "" {
		t.Skip("set CHRONO_PRESENTER_SOAK=30m for the synthetic release soak")
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		t.Fatal("invalid soak duration")
	}
	f := newLiveFinishFixture(t, 4096)
	ts := httptest.NewServer(f.server.routes())
	defer ts.Close()
	url := ts.URL + "/api/live/finishes/stream"
	a := openLoadFinishClient(t, url, true)
	defer a.close()
	b := openLoadFinishClient(t, url, true)
	defer b.close()
	slow := openLoadFinishClient(t, url, false)
	defer func() { slow.close() }()
	s := f.server
	s.mu.Lock()
	h := s.finishHub
	s.mu.Unlock()
	var rawBefore int
	if err := f.store.DB().QueryRow(`SELECT COUNT(*) FROM rfid_logs`).Scan(&rawBefore); err != nil {
		t.Fatal(err)
	}
	profileDir := os.Getenv("CHRONO_PRESENTER_SOAK_DIR")
	warm := finishResourceProfile(t, profileDir, "start")
	idleDuration := duration / 10
	if idleDuration > 30*time.Second {
		idleDuration = 30 * time.Second
	}
	idleBuilds := h.builds.Load()
	idleVerified := false
	started := time.Now()
	timer := time.NewTimer(duration)
	defer timer.Stop()
	ticks := time.NewTicker(10 * time.Millisecond)
	defer ticks.Stop()
	flap := time.NewTicker(2 * time.Second)
	defer flap.Stop()
	profiles := time.NewTicker(time.Minute)
	defer profiles.Stop()
	var reads, finishes, profileNumber int
	send := func(member int, sidecar bool) {
		p, ack := f.frame(member, sidecar)
		if err := f.exchange(p, ack); err != nil {
			t.Fatal(err)
		}
		reads++
	}
	for {
		select {
		case err := <-a.errors:
			t.Fatal(err)
		case err := <-b.errors:
			t.Fatal(err)
		case <-timer.C:
			freshnessDeadline := time.Now().Add(2 * time.Second)
			for finishes > 0 && (a.lastNumber.Load() != int64(1000+finishes) || b.lastNumber.Load() != int64(1000+finishes)) && time.Now().Before(freshnessDeadline) {
				time.Sleep(time.Millisecond)
			}
			if finishes > 0 && (a.lastNumber.Load() != int64(1000+finishes) || b.lastNumber.Load() != int64(1000+finishes)) {
				t.Fatal("normal clients did not receive the final committed finish")
			}
			if !idleVerified {
				t.Fatal("idle phase was not verified")
			}
			slow.close()
			a.close()
			b.close()
			waitFinishClients(t, h, 0)
			if f.store.ReadStats().InUse != 0 {
				t.Fatal("soak retained read leases")
			}
			var raw int
			if err := f.store.DB().QueryRow(`SELECT COUNT(*) FROM rfid_logs`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if raw-rawBefore != reads {
				t.Fatalf("lost/duplicated input: raw=%d expected=%d", raw-rawBefore, reads)
			}
			if f.manager.Status("100").Errors != 0 {
				t.Fatal("RFID errors during soak")
			}
			final := finishResourceProfile(t, profileDir, "final")
			// Bounded SQLite/ranker caches and4096 fixed registrations permit
			// population growth; client-only leak regression has a stricter2MiB gate.
			if duration >= 30*time.Minute && int64(final.HeapAlloc)-int64(warm.HeapAlloc) > 32*1024*1024 {
				t.Fatal("soak retained heap exceeds frozen32MiB budget")
			}
			t.Logf("duration=%s release_soak=%t reads=%d finishes=%d rate=%.2f/s snapshots=%d/%d builds=%d heap_delta=%d goroutines=%d", time.Since(started), duration >= 30*time.Minute, reads, finishes, float64(reads)/time.Since(started).Seconds(), a.snapshots.Load(), b.snapshots.Load(), h.builds.Load(), int64(final.HeapAlloc)-int64(warm.HeapAlloc), runtime.NumGoroutine())
			return
		case <-ticks.C:
			idle := time.Since(started) < idleDuration
			if !idle && !idleVerified {
				if h.builds.Load() != idleBuilds {
					t.Fatal("raw-only idle phase rebuilt presenter snapshots")
				}
				idleVerified = true
			}
			member := 0
			if !idle && (reads+1)%100 == 0 {
				finishes++
				member = finishes
			}
			send(member, reads%2 == 0)
			if reads%6000 == 0 {
				for j := 0; j < 30; j++ {
					finishes++
					send(finishes, j%2 == 0)
				}
			}
		case <-flap.C:
			if time.Since(started) < idleDuration {
				continue
			}
			slow.close()
			slow = openLoadFinishClient(t, url, false)
		case <-profiles.C:
			profileNumber++
			stats := finishResourceProfile(t, profileDir, fmt.Sprintf("minute-%02d", profileNumber))
			if profileNumber == 1 {
				warm = stats
			}
			t.Logf("minute=%d reads=%d finishes=%d heap=%d goroutines=%d workers=%d", profileNumber, reads, finishes, stats.HeapAlloc, runtime.NumGoroutine(), h.activeWorkers.Load())
		}
	}
}
