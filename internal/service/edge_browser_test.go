//go:build linux && edgeintegration && edgebrowser

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The browser drives the actual sidecar's embedded UI/API. Reuse the existing
// source fixture; no frontend mock, vendor directory or clock write is involved.
func TestPlateLocalBrowserWorkflow(t *testing.T) {
	node := edgeChainBinary(t, "EDGE_NODE_BINARY")
	browser := edgeChainBinary(t, "EDGE_BROWSER_BINARY")
	artifacts := t.TempDir()
	if root := os.Getenv("EDGE_BROWSER_ARTIFACTS"); root != "" {
		if !filepath.IsAbs(root) {
			t.Fatal("EDGE_BROWSER_ARTIFACTS must be an existing absolute directory")
		}
		var err error
		artifacts, err = os.MkdirTemp(root, "plate-browser-")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("retained synthetic browser artifacts: %s", artifacts)
	}
	source := newEdgeChainSource(t, "plate", browserOfflinePeer(t), browserOfflinePeer(t))
	source.start(t)
	run := func(phase string, reference time.Time) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Second)
		defer cancel()
		script, err := filepath.Abs("testdata/plate-browser-smoke.mjs")
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(ctx, node, script, "http://"+source.admin, phase, reference.UTC().Format(time.RFC3339Nano), artifacts)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "TZ=UTC", "EDGE_BROWSER_BINARY=" + browser}
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("browser %s: %v\n%s", phase, err, output)
		}
		t.Logf("browser %s: %s", phase, output)
	}
	run("configure", time.Time{})
	source.read(t, 1)
	source.waitRetry(t, "hub")
	source.waitRetry(t, "chrono")
	before := edgeSwitchRows(t, source)
	if len(before) != 1 {
		t.Fatalf("initial UI-configured capture count=%d", len(before))
	}
	source.stop(t)
	source.start(t)
	// A 12-hour-old time-only reading is unresolved even on a development
	// host that happens to obtain fresh verified OS synchronization after boot.
	reference := time.Now().UTC().Add(-12 * time.Hour).Truncate(time.Millisecond)
	conn := source.connectReader(t)
	_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
	_, err := fmt.Fprintf(conn, "{\"RTC\":%q,\"EPC\":\"E2000002\",\"ANT\":1}\n", reference.Format("15:04:05.000"))
	_ = conn.Close()
	if err != nil {
		t.Fatal(err)
	}
	edgeChainWait(t, "undated raw input visible to browser", func() bool {
		data, code, err := source.request(http.MethodGet, "/api/status", nil)
		var status struct {
			Journal struct{ Captured, Unresolved int } `json:"journal"`
		}
		return err == nil && code == 200 && json.Unmarshal(data, &status) == nil && status.Journal.Captured == 2 && status.Journal.Unresolved == 1
	})
	run("review", reference)
	after := edgeSwitchRows(t, source)
	if len(after) != 2 {
		t.Fatalf("reviewed source facts=%d", len(after))
	}
	for id, row := range before {
		if after[id] != row {
			t.Fatal("browser date review rewrote an existing observation")
		}
	}
	source.stop(t)
	source.start(t)
	run("verify", reference)
	source.stop(t)
}

// Reserve each address throughout the test. Connection rejection exercises
// visible durable retry queues without implementing any synthetic wire ACK.
func browserOfflinePeer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	return listener.Addr().String()
}
