//go:build linux && edgeintegration && edgeload

package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.com/fightmaster1/rfid-core/edge"
	"gitlab.com/fightmaster1/rfid-core/ingest"
	"gitlab.com/fightmaster1/rfid-core/tcp"
)

// This separate gate measures only the actual sidecar subprocess. The two
// core-backed synthetic peers deliberately exclude Hub/Desk storage/projection
// costs. It is not receiver throughput or Raspberry Pi acceptance.
func TestEdgeSourceSustainedLoadAndBacklog(t *testing.T) {
	for _, profile := range []string{"feibot", "plate"} {
		t.Run(profile, func(t *testing.T) {
			hubAddress, hub := newEdgeLoadPeer(t)
			deskAddress, desk := newEdgeLoadPeer(t)
			source := newEdgeChainSource(t, profile, hubAddress, deskAddress)
			if profile == "feibot" {
				// The tiny three-observation fixture uses 100. Use the actual
				// shipped default of 500, not a throughput-specific tuning value.
				data, err := os.ReadFile(source.config)
				if err != nil || strings.Count(string(data), "batch_size = 100\n") != 1 {
					t.Fatalf("load fixture batch setting: %v", err)
				}
				data = []byte(strings.Replace(string(data), "batch_size = 100\n", "batch_size = 500\n", 1))
				if err := os.WriteFile(source.config, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			source.start(t)
			if profile == "plate" {
				source.confirmClock(t)
			}
			var resources edgeLoadResources
			resources.sample(t, source)
			writer := newEdgeLoadInput(t, source)
			const batch, batches, offlineAt = 25, 600, 300
			const count = batch * batches
			started := time.Now()
			var offlineCount int
			var maxHealthyLag, maxLate time.Duration
			for i := 0; i < batches; i++ {
				due := started.Add(time.Duration(i) * 100 * time.Millisecond)
				if delay := time.Until(due); delay > 0 {
					time.Sleep(delay)
				}
				maxLate = max(maxLate, time.Since(due))
				if i == offlineAt {
					desk.mu.Lock()
					desk.offline = true
					offlineCount = len(desk.packets)
					desk.mu.Unlock()
				}
				writer.write(t, i*batch+1, batch)
				if i%10 == 9 {
					resources.sample(t, source)
					hub.mu.Lock()
					lag := (i+1)*batch - len(hub.packets)
					hub.mu.Unlock()
					maxHealthyLag = max(maxHealthyLag, time.Duration(lag)*time.Second/250)
				}
			}
			writer.close(t)
			feedDuration := time.Since(started)
			t.Logf("input: %d reads in %s (%.1f/s), maximum schedule delay=%s, healthy-peer lag=%s", count, feedDuration, float64(count)/feedDuration.Seconds(), maxLate, maxHealthyLag)
			resources.sample(t, source)
			t.Logf("at input end: peak RSS=%.2f MiB, threads=%d, FDs=%d, DB+WAL+SHM=%.2f MiB, logs=%.2f KiB", float64(resources.rssKiB)/1024, resources.threads, resources.fds, float64(resources.dbBytes)/(1<<20), float64(resources.logBytes)/1024)
			logEdgeLoadProgress(t, source)
			// A blocked writer must not turn a claimed 250/s test into a
			// silently slower replay. These are local smoke limits, not Pi SLOs.
			if feedDuration > 65*time.Second || maxLate > 2*time.Second || maxHealthyLag > 10*time.Second {
				t.Error("source did not sustain the configured live input/healthy delivery rate")
			}
			waitEdgeLoad(t, source, &resources, 30*time.Second, "healthy peer ACKs and durable capture", func() bool {
				st, err := source.status()
				if err != nil {
					return false
				}
				for _, d := range st.Destinations {
					if d.ID == "hub" {
						return d.Acknowledged == count
					}
				}
				return false
			})
			before := edgeSwitchRows(t, source)
			if len(before) != count {
				t.Fatalf("persisted facts=%d want=%d", len(before), count)
			}
			desk.mu.Lock()
			unchangedOffline := len(desk.packets) == offlineCount
			desk.mu.Unlock()
			if !unchangedOffline || offlineCount == 0 || offlineCount >= count {
				t.Fatalf("invalid outage evidence: accepted before outage=%d, unchanged=%v", offlineCount, unchangedOffline)
			}
			source.waitRetry(t, "chrono")
			source.stop(t)
			cpu := source.process.ProcessState.UserTime() + source.process.ProcessState.SystemTime()
			hub.mu.Lock()
			hubCalls := hub.calls
			hub.mu.Unlock()
			desk.mu.Lock()
			desk.offline = false
			desk.mu.Unlock()
			recovery := time.Now()
			source.start(t)
			waitEdgeLoad(t, source, &resources, 90*time.Second, "backlog ACKs without new input", func() bool {
				st, err := source.status()
				if err != nil || len(st.Destinations) != 2 {
					return false
				}
				for _, d := range st.Destinations {
					if d.Acknowledged != count || d.Pending+d.Retry != 0 {
						return false
					}
				}
				return true
			})
			recovered := time.Since(recovery)
			resources.sample(t, source)
			source.stop(t)
			cpu += source.process.ProcessState.UserTime() + source.process.ProcessState.SystemTime()
			after := edgeSwitchRows(t, source)
			if len(after) != len(before) {
				t.Fatal("restart changed observation count")
			}
			for id, row := range before {
				if after[id] != row {
					t.Fatalf("restart rewrote original source observation %s", id)
				}
				packet, err := edge.Decode([]byte(row.Payload))
				if err != nil {
					t.Fatal(err)
				}
				data, err := json.Marshal(packet)
				if err != nil {
					t.Fatal(err)
				}
				want := sha256.Sum256(data)
				for _, peer := range []*edgeLoadPeer{hub, desk} {
					peer.mu.Lock()
					got, ok := peer.packets[id]
					peer.mu.Unlock()
					if !ok || got != want {
						t.Fatalf("peer lost or changed source observation %s", id)
					}
				}
			}
			hub.mu.Lock()
			if hub.calls != hubCalls || len(hub.packets) != count || hub.mismatch {
				t.Errorf("healthy destination replay/mismatch: calls=%d before=%d unique=%d", hub.calls, hubCalls, len(hub.packets))
			}
			hub.mu.Unlock()
			desk.mu.Lock()
			if len(desk.packets) != count || desk.mismatch {
				t.Errorf("recovered destination mismatch: unique=%d", len(desk.packets))
			}
			desk.mu.Unlock()
			t.Logf("source only: peak RSS=%.2f MiB, threads=%d, FDs=%d, DB+WAL+SHM=%.2f MiB, logs=%.2f KiB, CPU=%s, total wall=%s, recovery=%s for %d queued reads", float64(resources.rssKiB)/1024, resources.threads, resources.fds, float64(resources.dbBytes)/(1<<20), float64(resources.logBytes)/1024, cpu, time.Since(started), recovered, count-offlineCount)
		})
	}
}

type edgeLoadPeer struct {
	mu                sync.Mutex
	offline, mismatch bool
	calls             int
	packets           map[string][32]byte
}

func (p *edgeLoadPeer) Publish(_ context.Context, event ingest.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.offline {
		return errors.New("synthetic receiver unavailable")
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	if old, ok := p.packets[event.ID]; ok && old != digest {
		p.mismatch = true
		return errors.New("source changed a previously accepted observation")
	}
	p.packets[event.ID] = digest
	p.calls++
	return nil
}

func newEdgeLoadPeer(t *testing.T) (string, *edgeLoadPeer) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	peer := &edgeLoadPeer{packets: make(map[string][32]byte)}
	pipeline := ingest.NewPipeline(peer, 1, 16, 2*time.Second)
	acceptDone := make(chan struct{})
	var workers sync.WaitGroup
	go func() {
		defer close(acceptDone)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				tcp.HandleConnWithContext(ctx, conn, tcp.ListenerConfig{Name: "synthetic-load", Adapter: tcp.EdgeAdapter{}, AckMode: tcp.AckModeID, ReadTimeout: 3 * time.Second}, pipeline)
			}()
		}
	}()
	t.Cleanup(func() { cancel(); _ = listener.Close(); <-acceptDone; workers.Wait(); pipeline.Close() })
	return listener.Addr().String(), peer
}

type edgeLoadInput struct {
	source *edgeChainSource
	conn   net.Conn
	file   *os.File
}

func newEdgeLoadInput(t *testing.T, source *edgeChainSource) *edgeLoadInput {
	t.Helper()
	w := &edgeLoadInput{source: source}
	if source.profile == "plate" {
		w.conn = source.connectReader(t)
	}
	t.Cleanup(func() { w.close(t) })
	return w
}

func (w *edgeLoadInput) write(t *testing.T, first, count int) {
	t.Helper()
	var batch strings.Builder
	now := time.Now().UTC()
	for n := first; n < first+count; n++ {
		if w.conn != nil {
			fmt.Fprintf(&batch, "{\"RTC\":%q,\"EPC\":\"E200%08X\",\"ANT\":1}\n", now.Format("15:04:05.000"), n)
		} else {
			fmt.Fprintf(&batch, "E200%08X:%s,port=1,rssi=28\n", n, now.Format("2006-01-02_15:04:05.000"))
		}
	}
	var target io.Writer
	if w.conn != nil {
		if err := w.conn.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		target = w.conn
	} else {
		// The vendor's 5,000-row rotation, with actual appends between scans.
		if (first-1)%5000 == 0 {
			w.close(t)
			path := filepath.Join(w.source.csvPath, fmt.Sprintf("U659_%d_00100.csv", (first-1)/5000+1))
			var err error
			w.file, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
		}
		target = w.file
	}
	if _, err := io.WriteString(target, batch.String()); err != nil {
		t.Fatal(err)
	}
}

func (w *edgeLoadInput) close(t *testing.T) {
	t.Helper()
	if w.conn != nil {
		_ = w.conn.Close()
		w.conn = nil
	}
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			t.Error(err)
		}
		w.file = nil
	}
}

type edgeLoadResources struct {
	rssKiB, threads, fds, dbBytes, logBytes int64
}

func (r *edgeLoadResources) sample(t *testing.T, source *edgeChainSource) {
	t.Helper()
	proc := fmt.Sprintf("/proc/%d", source.process.Process.Pid)
	data, err := os.ReadFile(filepath.Join(proc, "status"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[0] != "VmHWM:" && fields[0] != "Threads:" {
			continue
		}
		value, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		if fields[0] == "VmHWM:" {
			r.rssKiB = max(r.rssKiB, value)
		} else {
			r.threads = max(r.threads, value)
		}
	}
	fds, err := os.ReadDir(filepath.Join(proc, "fd"))
	if err != nil {
		t.Fatal(err)
	}
	r.fds = max(r.fds, int64(len(fds)))
	var dbBytes, logBytes int64
	files, err := os.ReadDir(source.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.IsDir() || (!strings.HasPrefix(file.Name(), "sidecar.db") && !strings.HasPrefix(file.Name(), "process-")) {
			continue
		}
		info, err := file.Info()
		if errors.Is(err, os.ErrNotExist) { // SQLite can remove a checkpointed WAL.
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(file.Name(), "sidecar.db") {
			dbBytes += info.Size()
		} else {
			logBytes += info.Size()
		}
	}
	r.dbBytes, r.logBytes = max(r.dbBytes, dbBytes), max(r.logBytes, logBytes)
}

func waitEdgeLoad(t *testing.T, source *edgeChainSource, resources *edgeLoadResources, limit time.Duration, label string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		resources.sample(t, source)
		if check() {
			return
		}
		time.Sleep(time.Second)
	}
	logEdgeLoadProgress(t, source)
	t.Fatalf("timed out: %s", label)
}

func logEdgeLoadProgress(t *testing.T, source *edgeChainSource) {
	t.Helper()
	db := edgeSwitchDB(t, source)
	defer db.Close()
	var reads, captures, resolutions, acked, pending int
	err := db.QueryRowContext(t.Context(), `SELECT (SELECT COUNT(*) FROM reads),
		(SELECT COUNT(*) FROM plate_raw_captures), (SELECT COUNT(*) FROM plate_resolutions),
		(SELECT COUNT(*) FROM deliveries WHERE state='acknowledged'),
		(SELECT COUNT(*) FROM deliveries WHERE state IN ('pending','retry'))`).Scan(&reads, &captures, &resolutions, &acked, &pending)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("durable source state: reads=%d plate_raw=%d plate_resolved=%d deliveries_ack=%d pending/retry=%d", reads, captures, resolutions, acked, pending)
}
