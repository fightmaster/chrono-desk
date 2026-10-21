package sqlite

import (
	"context"
	"io"
	"log"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/processor"
)

func TestLastWindowLiveReplaysPersistedMemberAfterFinish(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	for _, query := range []string{
		"INSERT INTO events(id,name) VALUES('e','Event')",
		"INSERT INTO races(id,event_id,name,started_at_ms) VALUES('r','e','Race',100000)",
		"INSERT INTO members(id,event_id,race_id,number,epc) VALUES('m','e','r',42,'AA')",
		"INSERT INTO checkpoints(id,event_id,race_id,type,sort,board,since_offset_seconds,until_offset_seconds,read_selection) VALUES('s','e','r',1,1,'B',0,30,'last')",
		"INSERT INTO checkpoints(id,event_id,race_id,type,sort,board,since_ms) VALUES('f','e','r',3,2,'B',200000)",
	} {
		if _, err := store.DB().Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	proc := processor.New(NewProcessorRepo(store), log.New(io.Discard, "", 0), false)
	for _, reading := range []struct {
		id string
		at int64
	}{{"s1", 100000}, {"f1", 300000}, {"s2-late", 125000}, {"outside", 130001}} {
		raw := domain.RfidLog{ID: reading.id, EventID: "e", Number: 42, EPC: "AA", Board: "B", TimeMs: reading.at}
		if _, err := store.DB().Exec("INSERT INTO rfid_logs(id,event_id,number,epc,board,time_ms) VALUES(?,?,?,?,?,?)", raw.ID, raw.EventID, raw.Number, raw.EPC, raw.Board, raw.TimeMs); err != nil {
			t.Fatal(err)
		}
		if err := proc.Process(ctx, raw, ""); err != nil {
			t.Fatal(err)
		}
	}
	var start, finish int64
	if err := store.DB().QueryRow("SELECT start_time_ms,finish_time_ms FROM members WHERE id='m'").Scan(&start, &finish); err != nil {
		t.Fatal(err)
	}
	if start != 125000 || finish != 300000 {
		t.Fatalf("start=%d finish=%d", start, finish)
	}
	var rawCount, passes int
	store.DB().QueryRow("SELECT COUNT(*) FROM rfid_logs").Scan(&rawCount)
	store.DB().QueryRow("SELECT COUNT(*) FROM results").Scan(&passes)
	if rawCount != 4 || passes != 2 {
		t.Fatalf("raw=%d passes=%d", rawCount, passes)
	}
}

func TestInvalidLastWindowEditRollsBack(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	for _, query := range []string{"INSERT INTO events(id,name) VALUES('e','Event')", "INSERT INTO races(id,event_id,name) VALUES('r','e','Race')", "INSERT INTO checkpoints(id,event_id,race_id,type,board,until_offset_seconds,read_selection) VALUES('s','e','r',1,'B',30,'last')"} {
		if _, err := store.DB().Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	err := store.WithinTx(ctx, func(tx *Store) error {
		_, err := tx.UpdateEntityField(ctx, "checkpoints", "until_offset_seconds", "s", nil)
		return err
	})
	if err == nil {
		t.Fatal("unbounded last accepted")
	}
	cp, err := store.GetCheckpoint(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if cp.UntilOffsetSeconds == nil || *cp.UntilOffsetSeconds != 30 {
		t.Fatal("invalid edit leaked")
	}
}

func TestLastWindowPreservesJudgeCheckpointPass(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	for _, query := range []string{
		"INSERT INTO events(id,name) VALUES('e','Event')",
		"INSERT INTO races(id,event_id,name,started_at_ms) VALUES('r','e','Race',100000)",
		"INSERT INTO members(id,event_id,race_id,number,epc) VALUES('m','e','r',42,'AA')",
		"INSERT INTO checkpoints(id,event_id,race_id,type,sort,board,since_offset_seconds,until_offset_seconds,read_selection) VALUES('s','e','r',1,1,'B',0,30,'last')",
		"INSERT INTO checkpoints(id,event_id,race_id,type,sort,board,since_ms) VALUES('f','e','r',3,2,'B',200000)",
		"INSERT INTO results(event_id,race_id,member_id,checkpoint_id,time_ms) VALUES('e','r','m','s',110000)",
		"INSERT INTO rfid_logs(id,event_id,number,epc,board,time_ms) VALUES('finish','e',42,'AA','B',300000)",
	} {
		if _, err := store.DB().Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	err := store.WithinTx(ctx, func(tx *Store) error { return tx.ReplayWindowMembers(ctx, "e", []string{"m"}) })
	if err != nil {
		t.Fatal(err)
	}
	var start int64
	if err := store.DB().QueryRow("SELECT start_time_ms FROM members WHERE id='m'").Scan(&start); err != nil {
		t.Fatal(err)
	}
	if start != 110000 {
		t.Fatal("judge start replaced")
	}
	var manual int
	store.DB().QueryRow("SELECT COUNT(*) FROM results WHERE rfid_log_id IS NULL").Scan(&manual)
	if manual != 1 {
		t.Fatal("manual source changed")
	}
}

func TestLegacyCheckpointUpsertDoesNotClearWindowSettings(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	for _, query := range []string{"INSERT INTO events(id,name) VALUES('e','Event')", "INSERT INTO races(id,event_id,name) VALUES('r','e','Race')", "INSERT INTO checkpoints(id,event_id,race_id,type,board,until_offset_seconds,read_selection) VALUES('s','e','r',1,'B',30,'last')"} {
		if _, err := store.DB().Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	legacy := domain.Checkpoint{ID: "s", EventID: "e", RaceID: "r", Type: domain.CheckpointStart, Board: "B"}
	if err := store.UpsertCheckpoint(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	cp, err := store.GetCheckpoint(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if cp.ReadSelection != "last" || cp.UntilOffsetSeconds == nil {
		t.Fatal("legacy absence cleared window")
	}
	legacy.WindowSettingsPresent = true
	legacy.ReadSelection = "first"
	if err := store.UpsertCheckpoint(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	cp, err = store.GetCheckpoint(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if cp.ReadSelection != "first" || cp.UntilOffsetSeconds != nil {
		t.Fatal("explicit clear was not applied")
	}
}
