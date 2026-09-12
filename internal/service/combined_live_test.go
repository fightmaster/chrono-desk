package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/rfid-core/edge"
	"gitlab.com/fightmaster1/rfid-core/ingest"
)

func TestCombinedListenerKeepsSourceAdmissionAndOutboxesSeparate(t *testing.T) {
	for _, edgeFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("edge_first_%t", edgeFirst), func(t *testing.T) {
			store, source := edgeLiveFixture(t)
			if _, err := store.DB().Exec(`DELETE FROM checkpoints`); err != nil {
				t.Fatal(err)
			}
			manager := NewLiveManager(log.New(io.Discard, "", 0))
			defer manager.StopAll()
			// Explicit board authorization derives the Feibot session; no
			// session text is copied from a device and no checkpoint is made.
			if err := manager.ConfigureEdge(t.Context(), store, "100", []domain.EdgeBinding{{Board: source.Board}}); err != nil {
				t.Fatal(err)
			}
			source.SourceSessionID = "100"
			port := edgeTestPort(t)
			if err := manager.StartCombined(store, "100", port); err != nil {
				t.Fatal(err)
			}
			if status := manager.Status("100"); !status.Edge.Combined || !status.Edge.Running {
				t.Fatalf("combined status: %+v", status)
			}
			roundTrip := func(payload []byte, expected string) {
				t.Helper()
				conn := edgeDial(t, port)
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(time.Second))
				if _, err := conn.Write(append(payload, '\n')); err != nil {
					t.Fatal(err)
				}
				if expected == "" {
					_ = conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
				}
				ack, err := bufio.NewReader(conn).ReadString('\n')
				if expected == "" {
					if ack != "" || err == nil {
						t.Fatalf("unauthorized/malformed observation ACK=%q err=%v", ack, err)
					}
				} else if ack != expected || err != nil {
					t.Fatalf("ACK=%q want=%q err=%v", ack, expected, err)
				}
			}
			native := []byte(fmt.Sprintf(`{"DeviceCode":"U659","time":%q,"epc":%q,"channelId":%d,"rssi":-50}`, source.RTC, source.EPC, source.Ant))
			owned, err := edge.Encode(source)
			if err != nil {
				t.Fatal(err)
			}
			roundTrip([]byte(`[{"DeviceCode":"U659","DeviceType":"Feibot","batteryPercent":78,"totalTagsRead":1240,"differentTagsRead":182,"Timestamp":"2026-09-10T08:20:00.000Z"}]`), "ok\n")
			if readers := manager.Status("100").Readers; len(readers) != 1 || readers[0].Device != "U659" || readers[0].BatteryPercent != 78 {
				t.Fatalf("combined input lost vendor monitoring: %+v", readers)
			}
			if edgeFirst {
				roundTrip(owned, string(edge.ACK(source)))
				roundTrip(native, "ok\n")
			} else {
				roundTrip(native, "ok\n")
				roundTrip(owned, string(edge.ACK(source)))
			}
			// One independent source observation must retain its own journal.
			second := source
			second.Ant++
			second.OriginSequence++
			second.ID = ingest.RFIDReadID(second.Board, second.EPC, second.Time, second.Ant)
			payload, _ := edge.Encode(second)
			roundTrip(payload, string(edge.ACK(second)))
			// Markers on valid vendor shapes may never escape via native ACK.
			poison := append(append([]byte(nil), native[:len(native)-1]...), []byte(`,"edge_version":null}`)...)
			roundTrip(poison, "")
			wrong := second
			wrong.SourceSessionID = "other"
			payload, _ = edge.Encode(wrong)
			roundTrip(payload, "")
			wrong = second
			wrong.ExternalEventID = 200
			payload, _ = edge.Encode(wrong)
			roundTrip(payload, "")
			var raw, nativeOutbox, edgeOutbox, results int
			if err := store.DB().QueryRow(`SELECT (SELECT COUNT(*) FROM rfid_logs),(SELECT COUNT(*) FROM observation_outbox),(SELECT COUNT(*) FROM edge_observation_outbox),(SELECT COUNT(*) FROM results)`).Scan(&raw, &nativeOutbox, &edgeOutbox, &results); err != nil {
				t.Fatal(err)
			}
			wantNative, wantEdge := 1, 1
			if edgeFirst {
				wantNative, wantEdge = 0, 2
			}
			if raw != 2 || nativeOutbox != wantNative || edgeOutbox != wantEdge || results != 0 {
				t.Fatalf("raw=%d native=%d edge=%d results=%d", raw, nativeOutbox, edgeOutbox, results)
			}
			items, err := store.EdgeJournal(t.Context(), "100", 0, 20)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range items {
				var stored ingest.Event
				if err := json.Unmarshal(item.Payload, &stored); err != nil || stored.OriginInstanceID != source.OriginInstanceID || stored.SourceSessionID != "100" {
					t.Fatal("combined receiver stole provenance or changed session")
				}
			}
			manager.StopEdge("100")
			if err := manager.ConfigureEdge(t.Context(), store, "100", nil); err != nil {
				t.Fatal(err)
			}
			if err := manager.StartCombined(store, "100", port); err == nil {
				t.Fatal("restarted combined reception with revoked bindings")
			}
		})
	}
}

func TestFeibotSessionResolutionDoesNotInferGenericOrPlateSessions(t *testing.T) {
	store, source := edgeLiveFixture(t)
	manager := NewLiveManager(log.New(io.Discard, "", 0))
	defer manager.StopAll()
	for _, board := range []string{"plate-test", "Feibot:", "Feibot: bad", "feibot:U659"} {
		if err := manager.ConfigureEdge(context.Background(), store, "100", []domain.EdgeBinding{{Board: board}}); err == nil {
			t.Fatalf("inferred unsupported board session %q", board)
		}
	}
	explicit := []domain.EdgeBinding{{Board: source.Board, SourceSessionID: "historical"}}
	if err := manager.ConfigureEdge(context.Background(), store, "100", explicit); err != nil {
		t.Fatal(err)
	}
	bindings, err := store.EdgeBindings(context.Background(), "100")
	if err != nil || len(bindings) != 1 || bindings[0] != explicit[0] || !strings.HasPrefix(bindings[0].Board, "Feibot:") {
		t.Fatal("explicit historical binding was rewritten")
	}
}
