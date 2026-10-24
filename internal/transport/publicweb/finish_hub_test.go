package publicweb

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/finishchange"
)

func nextTestHubFrame(t *testing.T, sub *finishSubscription) *finishFrame {
	t.Helper()
	select {
	case <-sub.wake:
		frame := sub.take()
		if frame == nil {
			t.Fatal("wake without pending snapshot")
		}
		return frame
	case <-sub.ctx.Done():
		t.Fatalf("subscription failed: %v", sub.failure())
	case <-time.After(2 * time.Second):
		t.Fatal("snapshot timed out")
	}
	return nil
}

func stopTestFinishHub(t *testing.T, h *finishHub) {
	t.Helper()
	h.cancel()
	select {
	case <-h.done:
	case <-time.After(2 * time.Second):
		t.Error("hub did not terminate")
	}
}

func TestFinishHubCommitDuringBuildAndFreshInitialSubscription(t *testing.T) {
	signal := finishchange.New()
	var version atomic.Int64
	version.Store(1)
	entered := make(chan struct{})
	release := make(chan struct{})
	var loads atomic.Int64
	h := newFinishHub("event", 1, signal, func(ctx context.Context, _ string) (publicFinishes, error) {
		v := version.Load()
		if loads.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return publicFinishes{}, ctx.Err()
			}
		}
		data := emptyFinishes()
		data.Published = true
		data.EventName = fmt.Sprint(v)
		return data, nil
	}, func(s string) string { return s })
	defer stopTestFinishHub(t, h)
	a, err := h.subscribe("")
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first build was not immediate")
	}
	b, err := h.subscribe("")
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	version.Store(2)
	signal.Notify()
	close(release)
	var data publicFinishes
	frame := nextTestHubFrame(t, b)
	if err := json.Unmarshal(frame.json, &data); err != nil {
		t.Fatal(err)
	}
	if data.EventName != "2" {
		t.Fatalf("reconnect received a snapshot pinned before subscription: %s", frame.json)
	}
	for data.EventName != "2" {
		if err := json.Unmarshal(nextTestHubFrame(t, a).json, &data); err != nil {
			t.Fatal(err)
		}
	}
	if loads.Load() < 2 {
		t.Fatal("commit during build was lost")
	}
}

func TestFinishHubSlowSubscriberKeepsOnlyLatestAndDoesNotBlockOthers(t *testing.T) {
	signal := finishchange.New()
	var version atomic.Int64
	h := newFinishHub("event", 1, signal, func(context.Context, string) (publicFinishes, error) {
		data := emptyFinishes()
		data.Published = true
		data.EventName = fmt.Sprint(version.Load())
		return data, nil
	}, func(s string) string { return s })
	defer stopTestFinishHub(t, h)
	fast, _ := h.subscribe("")
	defer fast.close()
	slow, _ := h.subscribe("")
	defer slow.close()
	nextTestHubFrame(t, fast)
	for i := 1; i <= 30; i++ {
		version.Store(int64(i))
		signal.Notify()
		var data publicFinishes
		for data.EventName != fmt.Sprint(i) {
			json.Unmarshal(nextTestHubFrame(t, fast).json, &data)
		}
	}
	var data publicFinishes
	json.Unmarshal(nextTestHubFrame(t, slow).json, &data)
	if data.EventName != "30" {
		t.Fatalf("slow client accumulated stale history: %+v", data)
	}
	slow.mu.Lock()
	pending := slow.pending
	slow.mu.Unlock()
	if pending != nil {
		t.Fatal("more than one pending snapshot")
	}
}

func TestFinishHubThousandReconnectsReleaseWorkersAndViews(t *testing.T) {
	h := newFinishHub("event", 1, finishchange.New(), func(context.Context, string) (publicFinishes, error) { return emptyFinishes(), nil }, func(s string) string { return s })
	defer stopTestFinishHub(t, h)
	var warm, final runtime.MemStats
	for i := 0; i < 1050; i++ {
		sub, err := h.subscribe("")
		if err != nil {
			t.Fatal(err)
		}
		nextTestHubFrame(t, sub)
		sub.close()
		h.workers.Wait()
		h.mu.Lock()
		clients, groups := h.clients, len(h.groups)
		h.mu.Unlock()
		if clients != 0 || groups != 0 || h.activeWorkers.Load() != 0 {
			t.Fatalf("cycle %d retained clients=%d groups=%d workers=%d", i, clients, groups, h.activeWorkers.Load())
		}
		if i == 49 {
			runtime.GC()
			runtime.ReadMemStats(&warm)
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&final)
	t.Logf("1000 reconnects: retained heap delta=%d bytes, heap objects delta=%d; owned clients/groups/workers=0", int64(final.HeapAlloc)-int64(warm.HeapAlloc), int64(final.HeapObjects)-int64(warm.HeapObjects))
}

func TestFinishHubCancellationDuringInitialBuildReleasesWorker(t *testing.T) {
	entered := make(chan struct{}, 1)
	h := newFinishHub("event", 1, finishchange.New(), func(ctx context.Context, _ string) (publicFinishes, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return publicFinishes{}, ctx.Err()
	}, func(s string) string { return s })
	defer stopTestFinishHub(t, h)
	for i := 0; i < 100; i++ {
		sub, err := h.subscribe("")
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("build did not start")
		}
		sub.close()
		h.workers.Wait()
		if h.activeWorkers.Load() != 0 {
			t.Fatal("cancelled initial build retained worker")
		}
	}
}
