package sqlite

import (
	"errors"
	"io"
	"log"
	"sync"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/processor"
)

func TestProcessorSerializesMixedIdentityProgressionTransitions(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	for _, statement := range []string{
		`INSERT INTO events (id, name) VALUES ('ev1', 'Event')`,
		`INSERT INTO races (id, event_id, name, started_at_ms) VALUES ('race1', 'ev1', 'Race', 500)`,
		`INSERT INTO members (id, event_id, race_id, number, epc) VALUES ('member1', 'ev1', 'race1', 42, 'CHIP-1')`,
		`INSERT INTO checkpoints (id, event_id, race_id, name, type, sort, board) VALUES ('cp1', 'ev1', 'race1', 'Split', 2, 1, 'board1')`,
		`INSERT INTO rfid_logs (id, event_id, number, epc, time_ms, board) VALUES ('by-number', 'ev1', 42, '', 1000, 'board1')`,
		`INSERT INTO rfid_logs (id, event_id, number, epc, time_ms, board) VALUES ('by-epc', 'ev1', 0, 'CHIP-1', 1001, 'board1')`,
	} {
		if _, err := store.DB().ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed %q: %v", statement, err)
		}
	}

	engine := processor.New(NewProcessorRepo(store), log.New(io.Discard, "", 0), false)
	logs := []domain.RfidLog{
		{ID: "by-number", EventID: "ev1", Number: 42, TimeMs: 1000, Board: "board1"},
		{ID: "by-epc", EventID: "ev1", EPC: "CHIP-1", TimeMs: 1001, Board: "board1"},
	}

	start := make(chan struct{})
	errors := make(chan error, len(logs))
	var workers sync.WaitGroup
	for _, entry := range logs {
		workers.Add(1)
		go func(logEntry domain.RfidLog) {
			defer workers.Done()
			<-start
			errors <- engine.Process(ctx, logEntry, "")
		}(entry)
	}
	close(start)
	workers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent Process() error = %v", err)
		}
	}

	var count int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM results WHERE member_id = 'member1' AND checkpoint_id = 'cp1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("once-checkpoint results = %d, want 1 after two concurrent identity paths", count)
	}
}

func TestProcessorRejectsAmbiguousMemberIdentity(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	for _, statement := range []string{
		`INSERT INTO events (id, name) VALUES ('ev1', 'Event')`,
		`INSERT INTO races (id, event_id, name) VALUES ('race1', 'ev1', 'Race 1')`,
		`INSERT INTO races (id, event_id, name) VALUES ('race2', 'ev1', 'Race 2')`,
		`INSERT INTO members (id, event_id, race_id, number) VALUES ('member1', 'ev1', 'race1', 42)`,
		`INSERT INTO members (id, event_id, race_id, number) VALUES ('member2', 'ev1', 'race2', 42)`,
	} {
		if _, err := store.DB().ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	_, found, err := NewProcessorRepo(store).LoadMember(ctx, "ev1", domain.RfidLog{Number: 42})
	if found || !errors.Is(err, ErrAmbiguousMemberIdentity) {
		t.Fatalf("found=%v err=%v, want ambiguity error", found, err)
	}
}
