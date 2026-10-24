package sqlite

import (
	"context"
	"errors"
	"testing"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/processor"
)

func finishChangeFixture(t *testing.T) *Store {
	t.Helper()
	s := newTestStore(t)
	ctx := context.Background()
	for _, err := range []error{
		s.UpsertEvent(ctx, domain.Event{ID: "event"}),
		s.UpsertRace(ctx, domain.Race{ID: "race", EventID: "event"}),
		s.UpsertMember(ctx, domain.Member{ID: "member", EventID: "event", RaceID: "race"}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestFinishChangesFollowOutermostCommit(t *testing.T) {
	s := finishChangeFixture(t)
	ctx := context.Background()
	signal := s.FinishChanges()
	initial := signal.Generation()
	clean := "00:01:00"
	err := s.WithinTx(ctx, func(tx *Store) error {
		if err := tx.WithinTx(ctx, func(nested *Store) error {
			return nested.UpdateMemberFinish(ctx, "member", nil, 60000, &clean)
		}); err != nil {
			return err
		}
		if signal.Generation() != initial {
			t.Fatal("notification before outer commit")
		}
		return tx.UpdateMemberFinish(ctx, "member", nil, 60001, &clean)
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := signal.Generation(); got != initial+1 {
		t.Fatalf("generation=%d, want %d", got, initial+1)
	}
	rollback := errors.New("rollback")
	err = s.WithinTx(ctx, func(tx *Store) error {
		if err := tx.UpdateMemberFinish(ctx, "member", nil, 70000, &clean); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if signal.Generation() != initial+1 {
		t.Fatal("rollback emitted change")
	}
}

func TestFinishChangesProcessorNestedCommitAndRollback(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[rollback], func(t *testing.T) {
			s := finishChangeFixture(t)
			ctx := context.Background()
			initial := s.FinishChanges().Generation()
			err := s.WithinTx(ctx, func(tx *Store) error {
				err := NewProcessorRepo(tx).WithTx(ctx, func(repo processor.TxRepository) (bool, error) {
					return true, repo.UpdateMemberTimes(ctx, processor.Member{ID: "member", RaceID: "race"}, processor.Checkpoint{Type: 3}, 60000, "observation")
				})
				if err != nil {
					return err
				}
				if s.FinishChanges().Generation() != initial {
					t.Fatal("joined processor notified before commit")
				}
				if rollback {
					return errors.New("reject outer transaction")
				}
				return nil
			})
			if (err != nil) != rollback {
				t.Fatal(err)
			}
			want := initial
			if !rollback {
				want++
			}
			if s.FinishChanges().Generation() != want {
				t.Fatalf("generation=%d want %d", s.FinishChanges().Generation(), want)
			}
		})
	}
}

func TestFinishChangesIgnoreRawAndIntermediateTraffic(t *testing.T) {
	s := finishChangeFixture(t)
	ctx := context.Background()
	initial := s.FinishChanges().Generation()
	if _, err := s.InsertRfidLogs(ctx, []domain.RfidLog{{ID: "raw", EventID: "event", Board: "fixture", TimeMs: 1000}}); err != nil {
		t.Fatal(err)
	}
	if err := NewProcessorRepo(s).WithTx(ctx, func(repo processor.TxRepository) (bool, error) {
		return true, repo.UpdateMemberTimes(ctx, processor.Member{ID: "member", RaceID: "race"}, processor.Checkpoint{Type: 2}, 1000, "raw")
	}); err != nil {
		t.Fatal(err)
	}
	if s.FinishChanges().Generation() != initial {
		t.Fatal("raw/intermediate input emitted finish change")
	}
	if err := NewProcessorRepo(s).WithTx(ctx, func(repo processor.TxRepository) (bool, error) {
		return true, repo.UpdateMemberTimes(ctx, processor.Member{ID: "member", RaceID: "race"}, processor.Checkpoint{Type: 3}, 60000, "raw")
	}); err != nil {
		t.Fatal(err)
	}
	if s.FinishChanges().Generation() != initial+1 {
		t.Fatal("standalone processor finish was not signalled")
	}
}

func TestFinishChangesIgnoreFailedCommitAndProcessorRollback(t *testing.T) {
	s := finishChangeFixture(t)
	ctx := context.Background()
	initial := s.FinishChanges().Generation()
	err := s.WithinTx(ctx, func(tx *Store) error {
		if _, err := tx.db.ExecContext(ctx, "PRAGMA defer_foreign_keys=ON"); err != nil {
			return err
		}
		return tx.UpsertMember(ctx, domain.Member{ID: "invalid", EventID: "event", RaceID: "missing"})
	})
	if err == nil {
		t.Fatal("expected deferred constraint failure at commit")
	}
	if s.FinishChanges().Generation() != initial {
		t.Fatal("failed commit notified")
	}
	// A deferred-FK commit failure leaves this driver's connection inside its
	// transaction (existing behavior, tracked separately as CHR-DESK-TX-001).
	// Test processor rollback independently rather than reuse that connection.
	s = finishChangeFixture(t)
	initial = s.FinishChanges().Generation()
	if err := NewProcessorRepo(s).WithTx(ctx, func(repo processor.TxRepository) (bool, error) {
		return false, repo.UpdateMemberTimes(ctx, processor.Member{ID: "member", RaceID: "race"}, processor.Checkpoint{Type: 3}, 60000, "raw")
	}); err != nil {
		t.Fatal(err)
	}
	if s.FinishChanges().Generation() != initial {
		t.Fatal("processor rollback notified")
	}
	member, err := s.GetMember(ctx, "member")
	if err != nil {
		t.Fatal(err)
	}
	if member.FinishTimeMs != nil {
		t.Fatal("processor rollback committed a finish")
	}
}
