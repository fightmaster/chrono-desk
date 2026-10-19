package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
)

func snapshotTestDigest(t *testing.T, store *sqlite.Store, query string) string {
	t.Helper()
	rows, err := store.DB().Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	for rows.Next() {
		values, dest := make([]any, len(cols)), make([]any, len(cols))
		for index := range values {
			dest[index] = &values[index]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		hash.Write(data)
		hash.Write([]byte{'\n'})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func seedSnapshotRecountTest(t *testing.T) *sqlite.Store {
	t.Helper()
	store := newTestStore(t)
	importFixture(t, store)
	if _, err := store.DB().Exec(`
		INSERT INTO results(event_id,race_id,member_id,time_ms) VALUES
		('ev-100','race-10k','mem-1',1780815000000),
		('ev-100','race-10k','mem-1',1780814900000);
		INSERT INTO rfid_logs(id,event_id,epc,board,time_ms,disabled_at) VALUES
		('wrong-hour','ev-100','E280AAA','Feibot:U659',1780809000000,1780900000000),
		('same-tag-later','ev-100','E280AAA','Feibot:U659',1780814000000,NULL)`); err != nil {
		t.Fatal(err)
	}
	return store
}

const snapshotPassDigestQuery = `SELECT member_id,race_id,checkpoint_id,rfid_log_id,time_ms,number FROM results ORDER BY member_id,race_id,time_ms,rfid_log_id`
const snapshotMemberDigestQuery = `SELECT id,start_time_ms,start_time_source,start_observation_id,finish_time_ms,clean_time,status FROM members ORDER BY id`

func runLegacySnapshotOracle(t *testing.T, store *sqlite.Store, scope ...string) {
	t.Helper()
	r := NewRecounter(store, log.New(io.Discard, "", 0), false)
	raceID := ""
	if len(scope) > 0 {
		raceID = scope[0]
	}
	if err := store.WithinTx(context.Background(), func(tx *sqlite.Store) error {
		_, err := r.recountLegacy(context.Background(), sqliteRecountTxStore{store: tx}, "ev-100", raceID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestScopedSnapshotRecountPreservesOutsideRaceAndItsReservedObservation(t *testing.T) {
	legacy, fast := seedSnapshotRecountTest(t), seedSnapshotRecountTest(t)
	for _, store := range []*sqlite.Store{legacy, fast} {
		if _, err := store.DB().Exec(`
			INSERT INTO races(id,event_id,name,format,started_at_ms) VALUES('other-race','ev-100','Other','FixedDistance',1780812000000);
			UPDATE members SET race_id='other-race',finish_time_ms=1780819000000 WHERE id='mem-2';
			INSERT INTO checkpoints(id,event_id,race_id,type,sort,board) VALUES('other-finish','ev-100','other-race',3,1,'Feibot:U659');
			INSERT INTO results(event_id,race_id,member_id,checkpoint_id,rfid_log_id,time_ms,number)
			VALUES('ev-100','other-race','mem-2','other-finish','log-active',1780812600000,102)`); err != nil {
			t.Fatal(err)
		}
	}
	outsidePasses := snapshotTestDigest(t, fast, `SELECT * FROM results WHERE race_id='other-race' ORDER BY id`)
	outsideMember := snapshotTestDigest(t, fast, `SELECT * FROM members WHERE race_id='other-race' ORDER BY id`)
	runLegacySnapshotOracle(t, legacy, "race-10k")
	stats, err := NewRecounter(fast, log.New(io.Discard, "", 0), false).Recount(context.Background(), "ev-100", "race-10k")
	if err != nil {
		t.Fatal(err)
	}
	if stats.EventReplayed || stats.RacesReplayed != 1 || stats.LogsReplayed != 4 {
		t.Fatalf("scope was broadened or read count changed: %+v", stats)
	}
	for _, query := range []string{snapshotPassDigestQuery, snapshotMemberDigestQuery} {
		if snapshotTestDigest(t, legacy, query) != snapshotTestDigest(t, fast, query) {
			t.Fatalf("scoped snapshot differs from legacy: %s", query)
		}
	}
	if snapshotTestDigest(t, fast, `SELECT * FROM results WHERE race_id='other-race' ORDER BY id`) != outsidePasses || snapshotTestDigest(t, fast, `SELECT * FROM members WHERE race_id='other-race' ORDER BY id`) != outsideMember {
		t.Fatal("race replay changed another race")
	}
}

func TestSnapshotRecountMatchesLegacyAndPreservesFacts(t *testing.T) {
	legacy, fast := seedSnapshotRecountTest(t), seedSnapshotRecountTest(t)
	raw := snapshotTestDigest(t, fast, `SELECT * FROM rfid_logs ORDER BY id`)
	manual := snapshotTestDigest(t, fast, `SELECT * FROM results WHERE rfid_log_id IS NULL ORDER BY id`)
	runLegacySnapshotOracle(t, legacy)
	stats, err := NewRecounter(fast, log.New(io.Discard, "", 0), false).Recount(context.Background(), "ev-100", "")
	if err != nil {
		t.Fatal(err)
	}
	if stats.LogsReplayed != 4 || !stats.EventReplayed {
		t.Fatalf("unexpected replay scope/count: %+v", stats)
	}
	for _, query := range []string{snapshotPassDigestQuery, snapshotMemberDigestQuery} {
		if snapshotTestDigest(t, legacy, query) != snapshotTestDigest(t, fast, query) {
			t.Fatalf("snapshot differs from the existing row replay: %s", query)
		}
	}
	if snapshotTestDigest(t, fast, `SELECT * FROM rfid_logs ORDER BY id`) != raw || snapshotTestDigest(t, fast, `SELECT * FROM results WHERE rfid_log_id IS NULL ORDER BY id`) != manual {
		t.Fatal("snapshot replay changed immutable raw/disable/manual facts")
	}
}

func TestSnapshotMaterializationFailureRollsBackEntireProjection(t *testing.T) {
	for name, trigger := range map[string]string{
		"pass":   `CREATE TRIGGER reject_batch BEFORE INSERT ON results WHEN NEW.rfid_log_id IS NOT NULL BEGIN SELECT RAISE(FAIL,'injected pass failure'); END`,
		"member": `CREATE TRIGGER reject_batch BEFORE UPDATE ON members WHEN NEW.finish_time_ms IS NOT NULL BEGIN SELECT RAISE(FAIL,'injected member failure'); END`,
	} {
		t.Run(name, func(t *testing.T) {
			store := seedSnapshotRecountTest(t)
			runLegacySnapshotOracle(t, store)
			passes, members := snapshotTestDigest(t, store, snapshotPassDigestQuery), snapshotTestDigest(t, store, snapshotMemberDigestQuery)
			if _, err := store.DB().Exec(trigger); err != nil {
				t.Fatal(err)
			}
			if _, err := NewRecounter(store, log.New(io.Discard, "", 0), false).Recount(context.Background(), "ev-100", ""); err == nil {
				t.Fatal("injected batch failure succeeded")
			}
			if snapshotTestDigest(t, store, snapshotPassDigestQuery) != passes || snapshotTestDigest(t, store, snapshotMemberDigestQuery) != members {
				t.Fatal("failed batch left a partially replaced protocol")
			}
		})
	}
}
