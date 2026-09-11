//go:build linux && edgeintegration && edgecentralintegration

package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

// Protocol-independent admission fencing uses the actual plate/Hub/sync path,
// real Laravel services and fresh MySQL migrations. MySQL wait edges, not
// elapsed sleeps or a hand-written substitute transaction, prove ordering.
func TestEdgeChainCentralPHPGoRevocationFence(t *testing.T) {
	runEdgeChainReceivers(t, "plate", func(t *testing.T, hub *edgeChainHub, source *edgeChainSource, store *sqlite.Store) {
		c := newEdgeChainCentral(t, hub, "plate-test", "plate-one")
		c.waitRows(t, 3)
		c.snapshot(t, "fence-setup")
		read := func(number int) {
			ep := fmt.Sprintf("E200%04d", number)
			n := int64(number)
			if err := store.UpsertMember(t.Context(), domain.Member{ID: strconv.Itoa(number), EventID: "100", RaceID: "race", Number: &n, EPC: &ep}); err != nil {
				t.Fatal(err)
			}
			source.read(t, number)
			source.waitACKs(t, number, number)
			assertEdgeChainRows(t, store, number)
		}
		assertUnchanged := func(before edgeChainCentralSnapshot) {
			after := c.snapshot(t, "snapshot")
			if !after.BindingEnabled || !reflect.DeepEqual(before.Rows, after.Rows) || !reflect.DeepEqual(before.Actions, after.Actions) || before.Results != after.Results || before.MemberResults != after.MemberResults {
				t.Fatalf("uncommitted transaction leaked state: before=%+v after=%+v", before, after)
			}
		}

		// The real consumer holds its admission share lock while a fixture-only
		// raw INSERT trigger waits for a locked InnoDB barrier row.
		before := c.snapshot(t, "snapshot")
		gateID, release := c.holdFence(t, 1)
		read(4)
		goID := c.waitBlockedBy(t, gateID, "edge_chain_test_gates")
		revoke := c.startPHP(t, "revoke")
		phpID := c.waitBlockedBy(t, goID, "rfid_source_sessions")
		assertUnchanged(before)
		release()
		revoke.wait(t, false)
		c.waitRows(t, 4)
		after := c.snapshot(t, "snapshot")
		if after.BindingEnabled || !reflect.DeepEqual(after.Actions, []string{"grant", "revoke"}) {
			t.Fatalf("Go-first admission/revoke did not commit in order: %+v", after)
		}
		t.Logf("Go-first: consumer connection %d held the grant until raw COMMIT; PHP connection %d waited, then revoked", goID, phpID)
		c.snapshot(t, "enable")

		// PHP-first commit and PHP audit-failure rollback exercise the same
		// binding lock in the other order. Both leave Hub/Desk acceptance live.
		for _, number := range []int{5, 6} {
			failAudit := number == 6
			if failAudit {
				c.snapshot(t, "fence-fail-audit")
			}
			before = c.snapshot(t, "snapshot")
			gateID, release = c.holdFence(t, 2)
			revoke = c.startPHP(t, "revoke")
			phpID = c.waitBlockedBy(t, gateID, "edge_chain_test_gates")
			read(number)
			goID = c.waitBlockedBy(t, phpID, "rfid_source_sessions")
			assertUnchanged(before)
			release()
			revoke.wait(t, failAudit)
			if failAudit {
				c.waitRows(t, number)
				after = c.snapshot(t, "snapshot")
				if !after.BindingEnabled || !reflect.DeepEqual(after.Actions, before.Actions) {
					t.Fatalf("failed audit did not roll back permission and audit together: %+v", after)
				}
				t.Logf("PHP audit rollback: consumer %d waited for PHP %d, then admitted unchanged source data under the retained grant", goID, phpID)
				continue
			}
			c.waitPendingRetry(t)
			after = c.snapshot(t, "snapshot")
			if after.BindingEnabled || !reflect.DeepEqual(after.Rows, before.Rows) || after.Results != before.Results || after.MemberResults != before.MemberResults || !reflect.DeepEqual(after.Actions, append(append([]string{}, before.Actions...), "revoke")) {
				t.Fatalf("Go admitted after a committed revoke: %+v", after)
			}
			publications := len(hub.entries(t))
			c.snapshot(t, "enable")
			c.waitRows(t, number)
			if len(hub.entries(t)) != publications {
				t.Fatal("re-enable recovery required a source resend")
			}
			t.Logf("PHP-first: consumer %d waited for PHP %d, retained the rejected packet in Redis, and recovered only after explicit enable", goID, phpID)
		}
		c.assertMetadata(t, store, c.snapshot(t, "snapshot"), 6)
	})
}

type edgeFencePHP struct {
	done   chan struct{}
	output []byte
	err    error
}

func (c *edgeChainCentral) startPHP(t *testing.T, action string, args ...string) *edgeFencePHP {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	p := &edgeFencePHP{done: make(chan struct{})}
	go func() {
		command := append([]string{"exec", c.phpID, "php", "tests/Support/edge-chain.php", action}, args...)
		p.output, p.err = exec.CommandContext(ctx, "docker", command...).CombinedOutput()
		close(p.done)
	}()
	t.Cleanup(func() { cancel(); <-p.done })
	return p
}

func (p *edgeFencePHP) wait(t *testing.T, failAudit bool) {
	t.Helper()
	<-p.done // The command has its own bounded context.
	if failAudit {
		if p.err == nil || !strings.Contains(string(p.output), "synthetic audit failure") {
			t.Fatalf("expected the injected PHP audit failure: %s %v", p.output, p.err)
		}
	} else if p.err != nil {
		t.Fatalf("PHP service failed: %s %v", p.output, p.err)
	}
}

func (c *edgeChainCentral) holdFence(t *testing.T, gate int) (int64, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", c.phpID, "php", "tests/Support/edge-chain.php", "fence-hold", strconv.Itoa(gate))
	input, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		_ = input.Close()
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		cancel()
		_ = input.Close()
		_ = output.Close()
		t.Fatal(err)
	}
	ready := make(chan string, 1)
	done := make(chan struct{})
	var waitErr error
	go func() {
		reader := bufio.NewReader(output)
		line, _ := reader.ReadString('\n')
		ready <- line
		_, _ = io.Copy(io.Discard, reader)
		waitErr = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() { _ = input.Close(); cancel(); <-done })
	var state struct {
		ConnectionID int64 `json:"connection_id"`
	}
	if line := <-ready; json.Unmarshal([]byte(line), &state) != nil || state.ConnectionID <= 0 {
		t.Fatalf("barrier did not acquire its row lock: %q", line)
	}
	return state.ConnectionID, func() {
		if _, err := io.WriteString(input, "release\n"); err != nil {
			t.Fatal(err)
		}
		_ = input.Close()
		<-done
		if waitErr != nil {
			t.Fatalf("barrier release: %s %v", stderr.String(), waitErr)
		}
	}
}

func (c *edgeChainCentral) waitBlockedBy(t *testing.T, blocker int64, table string) int64 {
	t.Helper()
	if blocker <= 0 || (table != "edge_chain_test_gates" && table != "rfid_source_sessions" && table != "edge_devices") {
		t.Fatal("invalid synthetic wait-edge selector")
	}
	var waiting int64
	edgeChainWait(t, "observed InnoDB wait on "+table, func() bool {
		query := fmt.Sprintf(`SELECT DISTINCT requesting.PROCESSLIST_ID
FROM performance_schema.data_lock_waits AS waits
JOIN performance_schema.threads AS requesting ON requesting.THREAD_ID=waits.REQUESTING_THREAD_ID
JOIN performance_schema.threads AS blocking ON blocking.THREAD_ID=waits.BLOCKING_THREAD_ID
JOIN performance_schema.data_locks AS locks ON locks.ENGINE=waits.ENGINE AND locks.ENGINE_LOCK_ID=waits.REQUESTING_ENGINE_LOCK_ID
WHERE blocking.PROCESSLIST_ID=%d AND locks.OBJECT_SCHEMA='synthetic_edge_chain' AND locks.OBJECT_NAME='%s'`, blocker, table)
		out := c.hub.docker(t, "exec", "--env", "MYSQL_PWD=synthetic-root", c.mysqlID, "mysql", "-uroot", "--batch", "--raw", "--skip-column-names", "-e", query)
		var err error
		waiting, err = strconv.ParseInt(strings.TrimSpace(out), 10, 64)
		return err == nil && waiting > 0 && waiting != blocker
	})
	return waiting
}

func (c *edgeChainCentral) waitPendingRetry(t *testing.T) {
	t.Helper()
	edgeChainWait(t, "central retry after committed PHP revoke", func() bool {
		out := c.hub.docker(t, "exec", c.hub.id, "redis-cli", "--json", "XPENDING", "synthetic-edge-chain", "synthetic-central", "-", "+", "10")
		var entries [][]json.RawMessage
		if err := json.Unmarshal([]byte(out), &entries); err != nil || len(entries) != 1 || len(entries[0]) != 4 {
			return false
		}
		var attempts int
		return json.Unmarshal(entries[0][3], &attempts) == nil && attempts >= 2
	})
}
