//go:build linux && edgeintegration && edgecentralintegration

package service

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// Real application services in separate PHP processes, ordered by observed
// InnoDB wait edges. HTTP/CSRF and actual sidecar retries have separate chains.
func TestEdgeManagementMySQLRevocationFence(t *testing.T) {
	hub := newEdgeChainHub(t, "plate-test", "plate-one")
	c := newEdgeChainCentral(t, hub, "plate-test", "plate-one")
	c.snapshot(t, "http-setup")
	c.snapshot(t, "management-setup")
	c.snapshot(t, "fence-setup")
	c.startPHP(t, "management-fence-trigger").wait(t, false)
	cases := []struct {
		name, holder, follower, audit, commandState string
		failAudit, queued, revoked, heartbeat       bool
		status                                      int
	}{
		{name: "revoke_before_enqueue", holder: "revoke", follower: "enqueue", audit: "revoke", revoked: true, status: 409},
		{name: "revoke_before_heartbeat", holder: "revoke", follower: "heartbeat", audit: "revoke", revoked: true, status: 401},
		{name: "enqueue_before_revoke", holder: "enqueue", follower: "revoke", audit: "enqueue", revoked: true, commandState: "cancelled", status: 200},
		{name: "delivery_before_revoke", holder: "heartbeat", follower: "revoke", audit: "deliver", queued: true, revoked: true, heartbeat: true, commandState: "outcome_unknown", status: 200},
		{name: "failed_revoke_then_enqueue", holder: "revoke", follower: "enqueue", audit: "revoke", failAudit: true, commandState: "queued", status: 200},
		{name: "failed_revoke_then_heartbeat", holder: "revoke", follower: "heartbeat", audit: "revoke", failAudit: true, heartbeat: true, status: 200},
		{name: "two_concurrent_enqueues", holder: "enqueue", follower: "enqueue", audit: "enqueue", commandState: "queued", status: 409},
	}
	for i, tc := range cases {
		if !t.Run(tc.name, func(t *testing.T) {
			slot := strconv.Itoa(i + 1)
			id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
			c.startPHP(t, "management-fence-mode", "off").wait(t, false)
			c.startPHP(t, "management-fence-prepare", slot).wait(t, false)
			if tc.queued {
				c.startPHP(t, "management-fence-enqueue", slot).wait(t, false)
			}
			failure := "0"
			if tc.failAudit {
				failure = "1"
			}
			c.startPHP(t, "management-fence-mode", tc.audit, failure).wait(t, false)
			before := c.managementSnapshot(t)
			barrier, release := c.holdFence(t, 2)
			holder := c.startPHP(t, "management-fence-"+tc.holder, slot)
			t.Cleanup(func() {
				if t.Failed() {
					select {
					case <-holder.done:
						t.Logf("holder outcome: %s %v", holder.output, holder.err)
					default:
					}
				}
			})
			holderID := c.waitBlockedBy(t, barrier, "edge_chain_test_gates")
			follower := c.startPHP(t, "management-fence-"+tc.follower, slot)
			followerID := c.waitBlockedBy(t, holderID, "edge_devices")
			if !reflect.DeepEqual(before, c.managementSnapshot(t)) {
				t.Fatal("uncommitted device/command/audit changes leaked")
			}
			release()
			holder.wait(t, tc.failAudit)
			follower.wait(t, false)
			var reply struct{ Status int }
			if err := json.Unmarshal(follower.output, &reply); err != nil || reply.Status != tc.status {
				t.Fatalf("contender result status=%d want=%d decode=%v", reply.Status, tc.status, err)
			}
			after := c.managementSnapshot(t)
			found := false
			for _, device := range after.Devices {
				if device.ID != id {
					continue
				}
				found = true
				request := strings.Repeat("a", 32)
				if tc.heartbeat {
					request = strings.Repeat("c", 32)
				}
				if (device.RevokedAt != nil) != tc.revoked || device.Snapshot["request_id"] != request {
					t.Fatal("revocation/snapshot visibility does not match committed ordering")
				}
			}
			if !found {
				t.Fatal("fixture device disappeared")
			}
			commands, revokes := 0, 0
			for _, command := range after.Commands {
				if command.DeviceID == id {
					commands++
					if command.State != tc.commandState || command.Result != nil {
						t.Fatal("command state does not match committed ordering")
					}
				}
			}
			for _, action := range after.Actions {
				if action.DeviceID == id && action.Action == "revoke" {
					revokes++
				}
			}
			wantCommands, wantRevokes := 0, 0
			if tc.commandState != "" {
				wantCommands = 1
			}
			if tc.revoked {
				wantRevokes = 1
			}
			if commands != wantCommands || revokes != wantRevokes || after.RawCount != before.RawCount || after.SourceCount != before.SourceCount {
				t.Fatal("command/audit count or timing admission changed unexpectedly")
			}
			t.Logf("observed MySQL waiter %d behind holder %d; contender status=%d, command=%s, revoked=%t", followerID, holderID, reply.Status, tc.commandState, tc.revoked)
		}) {
			return
		}
	}
}
