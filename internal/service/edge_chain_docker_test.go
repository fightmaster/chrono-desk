//go:build linux && edgeintegration

package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type edgeChainHub struct{ id, endpoint string }

func edgeChainDocker(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	output, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func (h *edgeChainHub) docker(t *testing.T, args ...string) string {
	t.Helper()
	output, err := edgeChainDocker(args...)
	if err != nil {
		t.Fatalf("local fixture Docker %v: %s: %v", args, output, err)
	}
	return output
}

func newEdgeChainHub(t *testing.T, board, session string) *edgeChainHub {
	t.Helper()
	return newEdgeChainHubForEvent(t, 100, board, session)
}

func newEdgeChainHubForEvent(t *testing.T, eventID int64, board, session string) *edgeChainHub {
	return newEdgeChainHubOnNetwork(t, eventID, board, session, "none")
}

// The two-event central test uses one private internal Docker network, without
// host port publication. Ordinary single-receiver fixtures keep network=none.
func newEdgeChainHubOnNetwork(t *testing.T, eventID int64, board, session, network string) *edgeChainHub {
	return newEdgeChainHubTopology(t, eventID, board, session, network, false)
}

func newEdgeChainHubTopology(t *testing.T, eventID int64, board, session, network string, mixed bool) *edgeChainHub {
	t.Helper()
	binary := edgeChainBinary(t, "EDGE_HUB_BINARY")
	image := os.Getenv("EDGE_REDIS_IMAGE")
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(image) {
		t.Fatal("EDGE_REDIS_IMAGE must be an already installed immutable Redis image ID; no pulls are allowed")
	}
	h := &edgeChainHub{}
	// Empty CWD, explicit environment and no container network prevent
	// accidentally discovering a real .env or contacting a real endpoint.
	name := "chr-side-002-chain-" + strings.ToLower(rand.Text())
	topology := []map[string]any{{
		"name": "edge-chain", "adapter": "edge_observation_v1", "host": "0.0.0.0", "port": "44004", "ack_mode": "id", "max_connections": 8,
		"edge_bindings": []map[string]any{{"board": board, "event_id": eventID, "source_session_id": session}},
	}}
	if mixed {
		topology = append(topology,
			map[string]any{"name": "myrace-mixed", "adapter": "myrace_nano", "port": "44002", "max_connections": 4, "publish_workers": 1, "publish_queue_size": 32},
			map[string]any{"name": "feibot-mixed", "adapter": "feibot", "port": "44003", "max_connections": 8, "publish_workers": 8, "publish_queue_size": 2048})
	}
	listeners, _ := json.Marshal(topology)
	h.id = h.docker(t, "create", "--pull", "never", "--name", name, "--label", "task=CHR-SIDE-002", "--network", network,
		"--user", strconv.Itoa(os.Getuid())+":"+strconv.Itoa(os.Getgid()),
		"--memory", "192m", "--cpus", "1", "--pids-limit", "64",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--tmpfs", "/data:rw,noexec,nosuid,size=32m",
		"--workdir", "/data",
		"--env", "REDIS_ADDR=127.0.0.1:6379", "--env", "REDIS_STREAM=synthetic-edge-chain", "--env", "TCP_LISTENERS_JSON="+string(listeners),
		"--entrypoint", "/bin/sh", image, "-c", `redis-server --bind 127.0.0.1 --save '' --appendonly no --dir /data & exec /tmp/rfid-hub`)
	t.Cleanup(func() {
		if t.Failed() {
			state, _ := edgeChainDocker("inspect", "--format", `running={{.State.Running}} exit={{.State.ExitCode}} bindings={{json .HostConfig.PortBindings}} ports={{json .NetworkSettings.Ports}}`, h.id)
			t.Logf("Hub/Redis fixture state: %s", state)
			logs, _ := edgeChainDocker("logs", "--tail", "60", h.id)
			t.Logf("Hub/Redis fixture log: %s", logs)
		}
		// Unpause is harmless if already running and allows ordinary cleanup
		// after an assertion fails during the deliberate outage.
		_, _ = edgeChainDocker("unpause", h.id)
		if output, err := edgeChainDocker("rm", "--force", h.id); err != nil {
			t.Errorf("remove owned fixture container: %s %v", output, err)
		}
	})
	// Copy through the Docker API: Docker Desktop need not share the host's
	// test temporary directory with its VM. No source tree is mounted.
	h.docker(t, "cp", binary, h.id+":/tmp/rfid-hub")
	h.docker(t, "start", h.id)
	edgeChainWait(t, "Hub and Redis fixture startup", func() bool {
		output, err := edgeChainDocker("exec", h.id, "/bin/sh", "-c", "redis-cli PING && /bin/busybox nc -z -w 1 127.0.0.1 44004")
		return err == nil && output == "PONG"
	})
	h.endpoint = h.tunnel(t)
	return h
}

// A transparent byte tunnel avoids Docker Desktop port/host-directory sharing.
// The real Hub TCP listener still parses, publishes and generates every ACK.
// No payload or acknowledgement is synthesized by this bridge.
func (h *edgeChainHub) tunnel(t *testing.T) string {
	return h.tunnelPort(t, "44004", nil)
}

func (h *edgeChainHub) tunnelPort(t *testing.T, port string, enabled *atomic.Bool) string {
	t.Helper()
	if port != "44002" && port != "44003" && port != "44004" {
		t.Fatal("unknown task-local Hub port")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			if enabled != nil && !enabled.Load() {
				_ = conn.Close()
				continue
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer conn.Close()
				stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
				defer stopClose()
				cmd := exec.CommandContext(ctx, "docker", "exec", "-i", h.id, "/bin/busybox", "nc", "127.0.0.1", port)
				cmd.Stderr = io.Discard
				input, err := cmd.StdinPipe()
				if err != nil {
					return
				}
				output, err := cmd.StdoutPipe()
				if err != nil {
					_ = input.Close()
					return
				}
				if err := cmd.Start(); err != nil {
					_ = input.Close()
					_ = output.Close()
					return
				}
				inputDone := make(chan struct{})
				go func() { defer close(inputDone); _, _ = io.Copy(input, conn); _ = input.Close() }()
				_, _ = io.Copy(conn, output)
				_ = conn.Close()
				_ = cmd.Wait()
				<-inputDone
			}()
		}
	}()
	t.Cleanup(func() { cancel(); _ = listener.Close(); <-done; workers.Wait() })
	return listener.Addr().String()
}

func (h *edgeChainHub) entries(t *testing.T) []map[string]string {
	t.Helper()
	output := h.docker(t, "exec", h.id, "redis-cli", "--json", "XRANGE", "synthetic-edge-chain", "-", "+")
	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		t.Fatal(err)
	}
	entries := make([]map[string]string, 0, len(raw))
	for _, item := range raw {
		var pair []json.RawMessage
		if err := json.Unmarshal(item, &pair); err != nil || len(pair) != 2 {
			t.Fatalf("invalid Redis entry: %s %v", item, err)
		}
		var fields []string
		if err := json.Unmarshal(pair[1], &fields); err != nil || len(fields)%2 != 0 {
			t.Fatalf("invalid Redis fields: %s %v", pair[1], err)
		}
		entry := map[string]string{}
		for i := 0; i < len(fields); i += 2 {
			entry[fields[i]] = fields[i+1]
		}
		entries = append(entries, entry)
	}
	return entries
}
