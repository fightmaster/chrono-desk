package service

import (
	"context"
	"fmt"
	"io"
	"log"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/rfid-core/ingest"
)

func TestEdgeRawAdmissionMultipleAntennasWithoutCheckpoint(t *testing.T) {
	ctx := context.Background()
	store, source := edgeLiveFixture(t)
	if _, err := store.DB().Exec(`DELETE FROM checkpoints`); err != nil {
		t.Fatal(err)
	}
	if err := store.SetEdgeBindings(ctx, "100", []domain.EdgeBinding{{Board: source.Board, SourceSessionID: source.SourceSessionID}}); err != nil {
		t.Fatal(err)
	}
	publisher := &edgePublisher{store: store, eventID: "100", stats: &LiveStats{}, logger: log.New(io.Discard, "", 0)}
	var observations []ingest.Event
	// Example channels, not a protocol limit or a mapping to TCP listeners.
	for _, antenna := range []int{1, 2, 3, 4, 5} {
		event := source
		event.Ant = antenna
		event.OriginSequence = uint64(antenna)
		event.ID = ingest.RFIDReadID(event.Board, event.EPC, event.Time, event.Ant)
		if err := publisher.Publish(ctx, event); err != nil {
			t.Fatal(err)
		}
		observations = append(observations, event)
	}
	var raw, results, outcomes, nativeOutbox int
	if err := store.DB().QueryRow(`SELECT (SELECT COUNT(*) FROM rfid_logs),(SELECT COUNT(*) FROM results),(SELECT COUNT(*) FROM members WHERE finish_time_ms IS NOT NULL),(SELECT COUNT(*) FROM observation_outbox)`).Scan(&raw, &results, &outcomes, &nativeOutbox); err != nil {
		t.Fatal(err)
	}
	if raw != len(observations) || results != 0 || outcomes != 0 || nativeOutbox != 0 {
		t.Fatalf("raw=%d results=%d outcomes=%d nativeOutbox=%d", raw, results, outcomes, nativeOutbox)
	}
	before, err := store.EdgeJournal(ctx, "100", 0, 50)
	if err != nil || len(before) != len(observations) {
		t.Fatalf("journal: %v %v", before, err)
	}
	for i := range observations {
		if err := store.UpsertCheckpoint(ctx, domain.Checkpoint{ID: fmt.Sprintf("logical-%d", i), EventID: "100", RaceID: "race", Board: source.Board, Type: domain.CheckpointType(2), Sort: int64(i + 1)}); err != nil {
			t.Fatal(err)
		}
	}
	// Later route configuration and retransmission cannot rewrite physical facts
	// or project an already accepted duplicate through a new route implicitly.
	for _, event := range observations {
		if err := publisher.Publish(ctx, event); err != nil {
			t.Fatal(err)
		}
		var board, epc, origin string
		var antenna int
		var instant int64
		if err := store.DB().QueryRow(`SELECT board,epc,ant,time_ms,origin_instance_id FROM rfid_logs WHERE id=?`, event.ID).Scan(&board, &epc, &antenna, &instant, &origin); err != nil {
			t.Fatal(err)
		}
		if board != event.Board || epc != event.EPC || antenna != event.Ant || instant != event.Time || origin != event.OriginInstanceID {
			t.Fatal("raw physical fields changed")
		}
	}
	after, err := store.EdgeJournal(ctx, "100", 0, 50)
	if err != nil || len(after) != len(before) {
		t.Fatalf("journal after mapping: %v %v", after, err)
	}
	for i := range before {
		if string(before[i].Payload) != string(after[i].Payload) {
			t.Fatal("mapping changed source payload/provenance")
		}
	}
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM results`).Scan(&results); err != nil || results != 0 {
		t.Fatalf("duplicate triggered new projection: %d %v", results, err)
	}
}
