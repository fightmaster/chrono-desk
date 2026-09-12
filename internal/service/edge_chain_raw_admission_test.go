//go:build linux && edgeintegration && edgecentralintegration

package service

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"gitlab.com/fightmaster1/rfid-core/edge"
	"gitlab.com/fightmaster1/rfid-core/ingest"
	"gitlab.com/fightmaster1/rfid-core/tcp"
)

func TestEdgeChainCentralRawWithoutCheckpoint(t *testing.T) {
	hub := newEdgeChainHub(t, "Feibot:U659", "100")
	central := newEdgeChainCentral(t, hub, "Feibot:U659", "100", "setup-raw")
	client := &tcp.LineClient{Endpoint: hub.endpoint, Timeout: 2 * time.Second}
	defer client.Close()
	instant := time.Now().UTC().Truncate(time.Millisecond)
	send := func(antenna int) ingest.Event {
		t.Helper()
		event := ingest.Event{EdgeVersion: 1, ExternalEventID: 100, SourceSessionID: "100", IdentityProfile: edge.IdentityRFID,
			ClockEvidenceID: "synthetic-source-clock", ClockQuality: edge.ClockSource, Board: "Feibot:U659", EPC: "E2000001",
			Time: instant.UnixMilli(), RTC: instant.Format(time.RFC3339Nano), Ant: antenna, RSSI: -50,
			ObservationVersion: 1, CaptureSourceID: "U659", OriginSystem: "edge", OriginInstanceID: "synthetic-raw-installation", OriginSequence: uint64(antenna)}
		event.ID = ingest.RFIDReadID(event.Board, event.EPC, event.Time, event.Ant)
		payload, err := edge.Encode(event)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.Send(t.Context(), payload, strings.TrimSuffix(string(edge.ACK(event)), "\n")); err != nil {
			t.Fatal(err)
		}
		// This fixture pauses for PHP/MySQL assertions between packets. Do not
		// reuse the test-only Docker byte tunnel past Hub's idle deadline.
		_ = client.Close()
		return event
	}
	waitRaw := func(count int) edgeChainCentralSnapshot {
		t.Helper()
		var snapshot edgeChainCentralSnapshot
		edgeChainWait(t, "raw-only central rows without outcomes", func() bool {
			snapshot = central.snapshot(t, "snapshot")
			if len(snapshot.Rows) != count || snapshot.Results != 0 || snapshot.MemberResults != 0 || snapshot.Finished != 0 {
				return false
			}
			fields := strings.Fields(hub.docker(t, "exec", hub.id, "redis-cli", "--raw", "XINFO", "GROUPS", "synthetic-edge-chain"))
			pending, lag := "", ""
			for i := 0; i+1 < len(fields); i++ {
				if fields[i] == "pending" {
					pending = fields[i+1]
				}
				if fields[i] == "lag" {
					lag = fields[i+1]
				}
			}
			return pending == "0" && lag == "0"
		})
		return snapshot
	}
	var events []ingest.Event
	for _, antenna := range []int{1, 2, 3} {
		events = append(events, send(antenna))
	}
	before := waitRaw(3)
	central.snapshot(t, "revoke")
	events = append(events, send(4))
	edgeChainWait(t, "revoked raw-only packet retried without central acceptance", func() bool {
		output := hub.docker(t, "exec", hub.id, "redis-cli", "--json", "XPENDING", "synthetic-edge-chain", "synthetic-central", "-", "+", "10")
		var entries [][]json.RawMessage
		if json.Unmarshal([]byte(output), &entries) != nil || len(entries) == 0 || len(entries[0]) != 4 {
			return false
		}
		var attempts int
		return json.Unmarshal(entries[0][3], &attempts) == nil && attempts >= 2
	})
	if held := central.snapshot(t, "snapshot"); !reflect.DeepEqual(before.Rows, held.Rows) || held.Results != 0 || held.MemberResults != 0 {
		t.Fatal("revocation changed raw input or outcomes")
	}
	central.snapshot(t, "enable")
	waitRaw(4)
	events = append(events, send(5))
	final := waitRaw(5)
	for _, event := range events {
		found := false
		for _, row := range final.Rows {
			if row["id"] != event.ID {
				continue
			}
			found = true
			if row["board"] != event.Board || row["epc"] != event.EPC || row["ant"] != json.Number(strconv.Itoa(event.Ant)) || row["time"] != json.Number(strconv.FormatInt(event.Time, 10)) || row["origin_instance_id"] != event.OriginInstanceID || row["source_session_id"] != event.SourceSessionID {
				t.Fatal("central raw physical fields or provenance changed")
			}
		}
		if !found {
			t.Fatal("missing raw identity")
		}
	}
	if !reflect.DeepEqual(final.Actions, []string{"grant", "revoke", "enable"}) {
		t.Fatalf("source audit: %v", final.Actions)
	}
	for antenna := 1; antenna <= 5; antenna++ {
		send(antenna)
	}
	after := waitRaw(5)
	if !reflect.DeepEqual(final.Rows, after.Rows) {
		t.Fatal("duplicate rewrote central raw observations")
	}
	t.Log("actual PHP registered-board grant + Hub scoped ACK + sync MySQL raw acceptance: 5 antennas, no checkpoints/outcomes; revoke/retry/enable retained provenance")
}
