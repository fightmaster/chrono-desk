//go:build linux && edgeintegration && edgecentralintegration

package service

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

func TestEdgeChainCentralEventSwitchPreservesHeldBacklog(t *testing.T) {
	for _, profile := range []string{"feibot", "plate"} {
		t.Run(profile, func(t *testing.T) {
			network := "chr-side-002-switch-" + strings.ToLower(rand.Text())
			docker := &edgeChainHub{}
			networkID := docker.docker(t, "network", "create", "--internal", "--driver", "bridge", "--label", "task=CHR-SIDE-002", network)
			t.Cleanup(func() {
				if out, err := edgeChainDocker("network", "rm", networkID); err != nil {
					t.Errorf("remove owned switch network: %s %v", out, err)
				}
			})
			if docker.docker(t, "network", "inspect", "--format", "{{.Internal}}", networkID) != "true" {
				t.Fatal("fixture network is not internal")
			}
			var oldCentral, newCentral *edgeChainCentral
			var oldInitial, newInitial edgeChainCentralSnapshot
			runEdgeChainEventSwitch(t, profile, edgeChainSwitchObserver{
				oldHub: func(t *testing.T, board, session string) *edgeChainHub {
					return newEdgeChainHubOnNetwork(t, 100, board, session, network)
				},
				setup: func(t *testing.T, hub *edgeChainHub, board, session string) {
					oldCentral = newEdgeChainCentral(t, hub, board, session)
					oldCentral.waitRows(t, 1)
					oldInitial = oldCentral.snapshot(t, "snapshot")
				},
				newHub: func(t *testing.T, oldHub *edgeChainHub, board, session string) *edgeChainHub {
					// Separate Hub namespaces avoid fixed HTTP/pprof port clashes;
					// only a private internal bridge links them to the same MySQL.
					oldCentral.snapshot(t, "switch-setup", board, session)
					hub := newEdgeChainHubOnNetwork(t, 200, board, session, network)
					var networks map[string]struct{ IPAddress string }
					data := oldHub.docker(t, "inspect", "--format", "{{json .NetworkSettings.Networks}}", oldHub.id)
					if err := json.Unmarshal([]byte(data), &networks); err != nil || len(networks) != 1 {
						t.Fatalf("unexpected fixture networks: %s %v", data, err)
					}
					address := networks[network].IPAddress
					if ip := net.ParseIP(address); ip == nil || !ip.IsPrivate() {
						t.Fatal("fixture MySQL address is not on its private network")
					}
					newCentral = &edgeChainCentral{hub: hub, phpID: oldCentral.phpID, mysqlID: oldCentral.mysqlID, eventID: "200", dbHost: address}
					newCentral.startConsumer(t)
					return hub
				},
				check: func(t *testing.T, phase string, oldStore, newStore *sqlite.Store) {
					newCentral.waitRows(t, 1)
					old := oldCentral.snapshot(t, "snapshot")
					next := newCentral.snapshot(t, "snapshot")
					if len(old.BoardEvents) != 1 || fmt.Sprint(old.BoardEvents[0]["event_id"]) != "200" {
						t.Fatal("fixture did not move the mutable native board mapping to event 200")
					}
					if phase == "selected" {
						newInitial = next
					}
					if phase != "recovered" {
						assertEdgeCentralStateUnchanged(t, oldInitial, old)
						oldCentral.assertMetadata(t, oldStore, old, 1)
						newCentral.assertMetadata(t, newStore, next, 1)
						t.Logf("central %s: old event=1, new event=1; old source backlog stays outside MySQL", phase)
						return
					}
					oldCentral.waitRows(t, 2)
					newCentral.waitRows(t, 1)
					old = oldCentral.snapshot(t, "snapshot")
					next = newCentral.snapshot(t, "snapshot")
					assertEdgeCentralStateUnchanged(t, newInitial, next)
					oldCentral.assertMetadata(t, oldStore, old, 2)
					newCentral.assertMetadata(t, newStore, next, 1)
					// Late old history must neither alter an earlier raw fact nor
					// finish a same-number/same-EPC member in the other event.
					found := false
					for _, row := range old.Rows {
						if row["id"] == oldInitial.Rows[0]["id"] {
							found = reflect.DeepEqual(row, oldInitial.Rows[0])
						}
					}
					if !found || len(old.FinishedRows) != 2 || len(next.FinishedRows) != 1 || fmt.Sprint(old.FinishedRows[0]["id"]) != "1" || fmt.Sprint(old.FinishedRows[1]["id"]) != "2" || fmt.Sprint(next.FinishedRows[0]["id"]) != "103" {
						t.Fatal("historical recovery changed existing raw data or crossed event/member ownership")
					}
					t.Log("central recovery: old=2/new=1 with separate results, feed/export and no Desk import echo; mutable board still points to new event")
				},
			})
		})
	}
}

func assertEdgeCentralStateUnchanged(t *testing.T, before, after edgeChainCentralSnapshot) {
	t.Helper()
	if !reflect.DeepEqual(before.Rows, after.Rows) || !reflect.DeepEqual(before.ResultRows, after.ResultRows) ||
		!reflect.DeepEqual(before.MemberResultRows, after.MemberResultRows) || !reflect.DeepEqual(before.FinishedRows, after.FinishedRows) {
		t.Fatal("another event's progress changed existing raw facts, passes or member outcomes")
	}
}
