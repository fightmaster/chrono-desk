package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

func TestReadSnapshotIsIndependentRepeatableAndCannotWrite(t *testing.T) {
	catalog, err := NewEventCatalog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	store, err := catalog.OpenOrCreate("event")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := store.UpsertEvent(ctx, domain.Event{ID: "event", Name: "committed"}); err != nil {
		t.Fatal(err)
	}
	writing, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- store.WithinTx(ctx, func(tx *Store) error {
			if err := tx.UpsertEvent(ctx, domain.Event{ID: "event", Name: "next"}); err != nil {
				return err
			}
			close(writing)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-writing:
	case <-ctx.Done():
		t.Fatal("writer did not start")
	}
	err = store.WithinReadSnapshot(ctx, func(read *Store) error {
		value, err := read.GetEvent(ctx, "event")
		if err != nil {
			return err
		}
		if value.Name != "committed" {
			t.Fatalf("reader saw uncommitted state: %+v", value)
		}
		close(release)
		if err := <-done; err != nil {
			return err
		}
		value, err = read.GetEvent(ctx, "event")
		if err != nil {
			return err
		}
		if value.Name != "committed" {
			t.Fatalf("one read view mixed commits: %+v", value)
		}
		if err := read.UpsertEvent(ctx, domain.Event{ID: "event", Name: "forbidden"}); err == nil {
			t.Fatal("query-only view wrote a fact")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WithinReadSnapshot(ctx, func(read *Store) error {
		value, err := read.GetEvent(ctx, "event")
		if err == nil && value.Name != "next" {
			t.Fatalf("next snapshot did not see the commit: %+v", value)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReadSnapshotInsideWriterUsesItsOwnTransaction(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if err := store.WithinTx(ctx, func(tx *Store) error {
		if err := tx.UpsertEvent(ctx, domain.Event{ID: "event", Name: "not committed"}); err != nil {
			return err
		}
		return tx.WithinReadSnapshot(ctx, func(read *Store) error {
			value, err := read.GetEvent(ctx, "event")
			if err == nil && value.Name != "not committed" {
				t.Fatalf("nested read escaped the transition fence: %+v", value)
			}
			return err
		})
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReadPoolIsBoundedQueryOnlyAndCancellable(t *testing.T) {
	catalog, err := NewEventCatalog(t.TempDir(), WithReadConnections(2))
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	store, err := catalog.OpenOrCreate("event")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	leases := make([]*sql.Conn, 0, 2)
	defer func() {
		for _, lease := range leases {
			lease.Close()
		}
	}()
	// Hold every connection simultaneously: each independently opened handle
	// must have its own query-only pragma, not just the first idle connection.
	for i := 0; i < 2; i++ {
		lease, err := store.readRoot.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		leases = append(leases, lease)
		var queryOnly int
		if err := lease.QueryRowContext(ctx, "PRAGMA query_only").Scan(&queryOnly); err != nil || queryOnly != 1 {
			t.Fatalf("reader %d is not query-only: %d %v", i, queryOnly, err)
		}
		if _, err := lease.ExecContext(ctx, "INSERT INTO events(id,name) VALUES('forbidden','forbidden')"); err == nil {
			t.Fatalf("reader %d can write", i)
		}
	}
	waiting, stopWaiting := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stopWaiting()
	if err := store.WithinReadSnapshot(waiting, func(*Store) error {
		t.Error("exhausted pool admitted a third reader")
		return nil
	}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pool wait ignored cancellation: %v", err)
	}
	if stats := store.ReadStats(); stats.OpenConnections != 2 || stats.WaitCount == 0 {
		t.Fatalf("reader pool is not bounded/observable: %+v", stats)
	}
	// A busy reader pool must not reserve the writer connection.
	if err := store.UpsertEvent(ctx, domain.Event{ID: "event", Name: "writer remains available"}); err != nil {
		t.Fatal(err)
	}
}
