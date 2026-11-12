package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/processor"
	modernsqlite "modernc.org/sqlite"
)

func TestFailedCommitDiscardsFinishAndAllowsWaitingWriter(t *testing.T) {
	for _, standalone := range []bool{false, true} {
		t.Run(fmt.Sprintf("processor=%v", standalone), func(t *testing.T) {
			s := finishChangeFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			initial := s.FinishChanges().Generation()
			next := make(chan error, 1)
			body := func(tx *Store) error {
				if _, err := tx.db.ExecContext(ctx, "PRAGMA defer_foreign_keys=ON"); err != nil {
					return err
				}
				if err := tx.UpsertMember(ctx, domain.Member{ID: "invalid", EventID: "event", RaceID: "missing"}); err != nil {
					return err
				}
				if err := tx.UpdateMemberFinish(ctx, "member", nil, 60000, nil); err != nil {
					return err
				}
				waits := s.root.Stats().WaitCount
				go func() {
					next <- s.WithinTx(ctx, func(tx *Store) error {
						return tx.UpsertEvent(ctx, domain.Event{ID: "next"})
					})
				}()
				for s.root.Stats().WaitCount == waits && ctx.Err() == nil {
					time.Sleep(time.Millisecond)
				}
				return ctx.Err()
			}
			var err error
			if standalone {
				err = NewProcessorRepo(s).WithTx(ctx, func(repo processor.TxRepository) (bool, error) {
					return true, body(repo.(*ProcessorRepo).store)
				})
			} else {
				err = s.WithinTx(ctx, body)
			}
			if err == nil {
				t.Fatal("expected deferred foreign-key COMMIT failure")
			}
			select {
			case err := <-next:
				if err != nil {
					t.Fatalf("next writer: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("next writer did not regain the connection")
			}
			member, err := s.GetMember(ctx, "member")
			if err != nil || member.FinishTimeMs != nil {
				t.Fatalf("failed commit leaked finish: %v, %v", member.FinishTimeMs, err)
			}
			if s.FinishChanges().Generation() != initial {
				t.Fatal("failed commit emitted finish notification")
			}
			var count int
			if err := s.root.QueryRowContext(ctx, "SELECT count(*) FROM members WHERE id='invalid'").Scan(&count); err != nil || count != 0 {
				t.Fatalf("failed commit leaked member: %d, %v", count, err)
			}
		})
	}
}

func TestTransactionCancellationAndPanicReleaseConnection(t *testing.T) {
	for _, mode := range []string{"cancel", "panic", "processor-panic"} {
		t.Run(mode, func(t *testing.T) {
			s := finishChangeFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			body := func(tx *Store) error {
				if err := tx.UpsertEvent(ctx, domain.Event{ID: "uncommitted"}); err != nil {
					return err
				}
				if mode == "cancel" {
					cancel()
					return nil
				}
				panic("abort transaction")
			}
			if mode == "cancel" {
				if err := s.WithinTx(ctx, body); err == nil {
					t.Fatal("cancelled transaction committed")
				}
			} else {
				func() {
					defer func() {
						if recover() != "abort transaction" {
							t.Error("panic was swallowed or changed")
						}
					}()
					if mode == "processor-panic" {
						_ = NewProcessorRepo(s).WithTx(ctx, func(repo processor.TxRepository) (bool, error) {
							return true, body(repo.(*ProcessorRepo).store)
						})
					} else {
						_ = s.WithinTx(ctx, body)
					}
				}()
			}
			check, stop := context.WithTimeout(context.Background(), 3*time.Second)
			defer stop()
			if err := s.WithinTx(check, func(tx *Store) error { return tx.UpsertEvent(check, domain.Event{ID: "next"}) }); err != nil {
				t.Fatalf("next writer: %v", err)
			}
			var count int
			if err := s.root.QueryRowContext(check, "SELECT count(*) FROM events WHERE id='uncommitted'").Scan(&count); err != nil || count != 0 {
				t.Fatalf("uncommitted data survived: %d, %v", count, err)
			}
		})
	}
}

var injectedDriverSequence atomic.Int64
var errInjectedCommit = errors.New("injected commit failure")

type commitFailureDriver struct {
	fail     atomic.Bool
	opens    atomic.Int64
	prepares atomic.Int64
}

func (d *commitFailureDriver) Open(name string) (driver.Conn, error) {
	c, err := (&modernsqlite.Driver{}).Open(name)
	if err != nil {
		return nil, err
	}
	d.opens.Add(1)
	return &commitFailureConn{Conn: c, owner: d}, nil
}

type commitFailureConn struct {
	driver.Conn
	owner *commitFailureDriver
}

func (c *commitFailureConn) Prepare(query string) (driver.Stmt, error) {
	c.owner.prepares.Add(1)
	return c.Conn.Prepare(query)
}

func (c *commitFailureConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *commitFailureConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &commitFailureTx{Tx: tx, fail: &c.owner.fail}, nil
}

type commitFailureTx struct {
	driver.Tx
	fail *atomic.Bool
}

func (tx *commitFailureTx) Commit() error {
	if tx.fail.Swap(false) {
		return errInjectedCommit
	}
	return tx.Tx.Commit()
}

func TestInjectedRawOwnershipCommitFailureIsAtomic(t *testing.T) {
	d := &commitFailureDriver{}
	name := fmt.Sprintf("commit-failure-%d", injectedDriverSequence.Add(1))
	sql.Register(name, d)
	db, err := sql.Open(name, "file:"+filepath.Join(t.TempDir(), "event.chrono")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	s, err := New(db, WithOriginInstanceID("desk-test"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.UpsertEvent(ctx, domain.Event{ID: "event"}); err != nil {
		t.Fatal(err)
	}
	logs := []domain.RfidLog{{ID: "raw", EventID: "event", Board: "test", CaptureSourceID: "test", TimeMs: 1000}}
	d.fail.Store(true)
	if n, err := s.InsertOwnedRfidLogs(ctx, logs); n != 0 || !errors.Is(err, errInjectedCommit) {
		t.Fatalf("injected outcome: %d, %v", n, err)
	}
	for _, table := range []string{"rfid_logs", "observation_outbox"} {
		var count int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s has partial write: %d, %v", table, count, err)
		}
	}
	if n, err := s.InsertOwnedRfidLogs(ctx, logs); n != 1 || err != nil {
		t.Fatalf("retry: %d, %v", n, err)
	}
	// Successful transactions reuse the connection and add no recovery SQL.
	opens, prepares := d.opens.Load(), d.prepares.Load()
	for range 2 {
		if err := s.WithinTx(ctx, func(tx *Store) error {
			return tx.UpsertEvent(ctx, domain.Event{ID: "healthy"})
		}); err != nil {
			t.Fatal(err)
		}
	}
	if d.opens.Load() != opens || d.prepares.Load()-prepares != 2 {
		t.Fatalf("healthy path changed: new connections=%d, statements=%d", d.opens.Load()-opens, d.prepares.Load()-prepares)
	}
}
