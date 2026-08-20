package processor

import (
	"context"
	"io"
	"log"
	"testing"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
)

// Port of rfid-sync's processor flow tests; the engine semantics must match
// the reference implementation, so these scenarios are kept 1:1 (minus
// board→event lookup and rfid_log insertion, which the desktop engine
// does not own).

type fakeProcessorRepository struct {
	resultExists       bool
	resultExistsErr    error
	rfidLogDisabled    bool
	rfidLogDisabledErr error
	member             Member
	memberFound        bool
	memberErr          error
	lastResult         LastResult
	lastResultErr      error
	passed             map[string]bool
	passedErr          error
	checkpoints        []Checkpoint
	checkpointsErr     error
	insertResultFn     func(checkpoint Checkpoint) (bool, error)
	updateMemberErr    error
	inTx               bool
	readOutsideTx      bool

	txCalls               int
	commitDecisions       []bool
	memberCalls           int
	insertedCheckpointIDs []string
	updateCheckpointIDs   []string
}

func (r *fakeProcessorRepository) ResultExists(context.Context, string) (bool, error) {
	r.readOutsideTx = r.readOutsideTx || !r.inTx
	return r.resultExists, r.resultExistsErr
}

func (r *fakeProcessorRepository) RfidLogDisabled(context.Context, string) (bool, error) {
	r.readOutsideTx = r.readOutsideTx || !r.inTx
	return r.rfidLogDisabled, r.rfidLogDisabledErr
}

func (r *fakeProcessorRepository) LoadMember(context.Context, string, domain.RfidLog) (Member, bool, error) {
	r.readOutsideTx = r.readOutsideTx || !r.inTx
	r.memberCalls++
	return r.member, r.memberFound, r.memberErr
}

func (r *fakeProcessorRepository) LoadLastResult(context.Context, string, string) (LastResult, error) {
	r.readOutsideTx = r.readOutsideTx || !r.inTx
	return r.lastResult, r.lastResultErr
}

func (r *fakeProcessorRepository) LoadPassedCheckpoints(context.Context, string, string) (map[string]bool, error) {
	r.readOutsideTx = r.readOutsideTx || !r.inTx
	return r.passed, r.passedErr
}

func (r *fakeProcessorRepository) LoadCheckpoints(context.Context, string, string) ([]Checkpoint, error) {
	r.readOutsideTx = r.readOutsideTx || !r.inTx
	return r.checkpoints, r.checkpointsErr
}

func (r *fakeProcessorRepository) WithTx(ctx context.Context, fn func(tx TxRepository) (bool, error)) error {
	r.txCalls++
	r.inTx = true
	defer func() { r.inTx = false }()
	commit, err := fn(r)
	if err != nil {
		return err
	}
	r.commitDecisions = append(r.commitDecisions, commit)
	return nil
}

func (r *fakeProcessorRepository) InsertResult(_ context.Context, _ domain.RfidLog, _ Member, checkpoint Checkpoint) (bool, error) {
	if r.insertResultFn != nil {
		inserted, err := r.insertResultFn(checkpoint)
		if inserted {
			r.insertedCheckpointIDs = append(r.insertedCheckpointIDs, checkpoint.ID)
		}
		return inserted, err
	}

	r.insertedCheckpointIDs = append(r.insertedCheckpointIDs, checkpoint.ID)
	return true, nil
}

func (r *fakeProcessorRepository) UpdateMemberTimes(_ context.Context, _ Member, checkpoint Checkpoint, _ int64) error {
	r.updateCheckpointIDs = append(r.updateCheckpointIDs, checkpoint.ID)
	return r.updateMemberErr
}

func newTestProcessor(repo Repository) *Processor {
	return New(repo, log.New(io.Discard, "", 0), false)
}

func ptr(v int64) *int64 { return &v }

func TestProcessSelectsFirstEligibleCheckpoint(t *testing.T) {
	raceStartMs := time.Date(2026, 3, 18, 10, 0, 0, 0, time.UTC).UnixMilli()
	checkpoints := []Checkpoint{
		{ID: "cp11", Sort: 1, Type: domain.CheckpointMid, SinceOffsetSeconds: ptr(60)},
		{ID: "cp22", Sort: 2, Type: domain.CheckpointMid, SinceOffsetSeconds: ptr(30)},
	}

	repo := &fakeProcessorRepository{
		member:      Member{ID: "m5", RaceID: "r9", RaceStartedAtMs: ptr(raceStartMs)},
		memberFound: true,
		passed:      map[string]bool{},
		checkpoints: checkpoints,
	}

	logEntry := domain.RfidLog{
		ID:      "rfid-log-1",
		EventID: "ev1",
		Number:  5858,
		TimeMs:  raceStartMs + 45_000,
		Board:   "board-1",
	}

	if err := newTestProcessor(repo).Process(context.Background(), logEntry, ""); err != nil {
		t.Fatalf("Process() error = %v", err)
	}

	if repo.txCalls != 1 {
		t.Fatalf("tx calls = %d, want 1", repo.txCalls)
	}
	if repo.readOutsideTx {
		t.Fatal("progression state was read outside the transaction")
	}
	if len(repo.commitDecisions) != 1 || !repo.commitDecisions[0] {
		t.Fatalf("commit decisions = %#v, want [true]", repo.commitDecisions)
	}
	if len(repo.insertedCheckpointIDs) != 1 || repo.insertedCheckpointIDs[0] != "cp22" {
		t.Fatalf("inserted checkpoints = %#v, want [cp22]", repo.insertedCheckpointIDs)
	}
	if len(repo.updateCheckpointIDs) != 1 || repo.updateCheckpointIDs[0] != "cp22" {
		t.Fatalf("updated checkpoints = %#v, want [cp22]", repo.updateCheckpointIDs)
	}
}

func TestProcessDoesNotCommitDuplicateResult(t *testing.T) {
	repo := &fakeProcessorRepository{
		member:      Member{ID: "m5", RaceID: "r9"},
		memberFound: true,
		passed:      map[string]bool{},
		checkpoints: []Checkpoint{{ID: "cp33", Sort: 3, Type: domain.CheckpointFinish}},
		insertResultFn: func(Checkpoint) (bool, error) {
			return false, nil
		},
	}

	logEntry := domain.RfidLog{
		ID:      "rfid-log-2",
		EventID: "ev1",
		Number:  5858,
		TimeMs:  time.Date(2026, 3, 18, 10, 1, 0, 0, time.UTC).UnixMilli(),
		Board:   "board-1",
	}

	if err := newTestProcessor(repo).Process(context.Background(), logEntry, ""); err != nil {
		t.Fatalf("Process() error = %v", err)
	}

	if repo.txCalls != 1 {
		t.Fatalf("tx calls = %d, want 1", repo.txCalls)
	}
	if len(repo.commitDecisions) != 1 || repo.commitDecisions[0] {
		t.Fatalf("commit decisions = %#v, want [false]", repo.commitDecisions)
	}
	if len(repo.updateCheckpointIDs) != 0 {
		t.Fatalf("updated checkpoints = %#v, want none", repo.updateCheckpointIDs)
	}
}

func TestProcessSkipsDisabledRfidLogWithoutRecreatingResult(t *testing.T) {
	repo := &fakeProcessorRepository{
		rfidLogDisabled: true,
		member:          Member{ID: "m5", RaceID: "r9"},
		memberFound:     true,
		passed:          map[string]bool{},
		checkpoints:     []Checkpoint{{ID: "cp33", Sort: 3, Type: domain.CheckpointFinish}},
	}

	logEntry := domain.RfidLog{
		ID:      "disabled-rfid-log",
		EventID: "ev1",
		Number:  5858,
		TimeMs:  time.Date(2026, 6, 2, 10, 1, 0, 0, time.UTC).UnixMilli(),
		Board:   "board-1",
	}

	if err := newTestProcessor(repo).Process(context.Background(), logEntry, ""); err != nil {
		t.Fatalf("Process() error = %v", err)
	}

	if repo.memberCalls != 0 {
		t.Fatalf("member loads = %d, want 0 (disabled source should stop before derivation)", repo.memberCalls)
	}
	if repo.txCalls != 1 || len(repo.commitDecisions) != 1 || repo.commitDecisions[0] {
		t.Fatalf("transaction = calls:%d commits:%v, want one rolled-back read transition", repo.txCalls, repo.commitDecisions)
	}
}

func TestProcessRecreatesEnabledRfidLogResultOnReplay(t *testing.T) {
	eventTimeMs := time.Date(2026, 6, 2, 10, 1, 0, 0, time.UTC).UnixMilli()
	repo := &fakeProcessorRepository{
		member: Member{
			ID:          "m5",
			RaceID:      "r9",
			Number:      ptr(5858),
			StartTimeMs: ptr(eventTimeMs - 60_000),
		},
		memberFound: true,
		passed:      map[string]bool{},
		checkpoints: []Checkpoint{{ID: "cp33", Sort: 3, Type: domain.CheckpointFinish}},
	}

	logEntry := domain.RfidLog{
		ID:      "enabled-rfid-log",
		EventID: "ev1",
		Number:  5858,
		TimeMs:  eventTimeMs,
		Board:   "board-1",
	}

	if err := newTestProcessor(repo).Process(context.Background(), logEntry, ""); err != nil {
		t.Fatalf("Process() error = %v", err)
	}

	if repo.txCalls != 1 {
		t.Fatalf("tx calls = %d, want 1", repo.txCalls)
	}
	if len(repo.insertedCheckpointIDs) != 1 || repo.insertedCheckpointIDs[0] != "cp33" {
		t.Fatalf("inserted checkpoints = %#v, want [cp33]", repo.insertedCheckpointIDs)
	}
}

func TestProcessSkipsCheckpointSleepingAfterPreviousResult(t *testing.T) {
	raceStartMs := time.Date(2026, 3, 18, 10, 0, 0, 0, time.UTC).UnixMilli()
	lastResultAtMs := raceStartMs + 2*60_000

	repo := &fakeProcessorRepository{
		member:      Member{ID: "m5", RaceID: "r9", RaceStartedAtMs: ptr(raceStartMs)},
		memberFound: true,
		lastResult:  LastResult{Sort: ptr(1), TimeMs: ptr(lastResultAtMs)},
		passed:      map[string]bool{},
		checkpoints: []Checkpoint{
			{ID: "cp22", Sort: 2, Type: domain.CheckpointMid, SleepAfterPrevSeconds: ptr(600)},
		},
	}

	// Read only one second after the previous отсечка: still sleeping.
	logEntry := domain.RfidLog{
		ID:      "rfid-log-asleep",
		EventID: "ev1",
		Number:  5858,
		TimeMs:  lastResultAtMs + 1000,
		Board:   "board-1",
	}

	if err := newTestProcessor(repo).Process(context.Background(), logEntry, ""); err != nil {
		t.Fatalf("Process() error = %v", err)
	}

	if repo.txCalls != 1 || len(repo.commitDecisions) != 1 || repo.commitDecisions[0] {
		t.Fatalf("transaction = calls:%d commits:%v, want one rolled-back read transition", repo.txCalls, repo.commitDecisions)
	}
}

func TestProcessAcceptsSleepingCheckpointWithoutPreviousResult(t *testing.T) {
	raceStartMs := time.Date(2026, 3, 18, 10, 0, 0, 0, time.UTC).UnixMilli()

	repo := &fakeProcessorRepository{
		member:      Member{ID: "m5", RaceID: "r9", RaceStartedAtMs: ptr(raceStartMs)},
		memberFound: true,
		// No previous result: lastResult fields are nil.
		passed: map[string]bool{},
		checkpoints: []Checkpoint{
			{ID: "cp22", Sort: 2, Type: domain.CheckpointMid, SleepAfterPrevSeconds: ptr(600)},
		},
	}

	logEntry := domain.RfidLog{
		ID:      "rfid-log-no-prev",
		EventID: "ev1",
		Number:  5858,
		TimeMs:  raceStartMs + 60_000,
		Board:   "board-1",
	}

	if err := newTestProcessor(repo).Process(context.Background(), logEntry, ""); err != nil {
		t.Fatalf("Process() error = %v", err)
	}

	if len(repo.insertedCheckpointIDs) != 1 || repo.insertedCheckpointIDs[0] != "cp22" {
		t.Fatalf("inserted checkpoints = %#v, want [cp22] (no previous отсечка → accept)", repo.insertedCheckpointIDs)
	}
}

func TestProcessSkipsCheckpointsAtOrBelowLastSort(t *testing.T) {
	repo := &fakeProcessorRepository{
		member:      Member{ID: "m5", RaceID: "r9"},
		memberFound: true,
		lastResult:  LastResult{Sort: ptr(2), TimeMs: ptr(int64(1000))},
		passed:      map[string]bool{},
		checkpoints: []Checkpoint{
			{ID: "cp11", Sort: 1, Type: domain.CheckpointMid},
			{ID: "cp22", Sort: 2, Type: domain.CheckpointMid},
			{ID: "cp33", Sort: 3, Type: domain.CheckpointFinish},
		},
	}

	logEntry := domain.RfidLog{
		ID:      "rfid-log-order",
		EventID: "ev1",
		Number:  5858,
		TimeMs:  5000,
		Board:   "board-1",
	}

	if err := newTestProcessor(repo).Process(context.Background(), logEntry, ""); err != nil {
		t.Fatalf("Process() error = %v", err)
	}

	if len(repo.insertedCheckpointIDs) != 1 || repo.insertedCheckpointIDs[0] != "cp33" {
		t.Fatalf("inserted checkpoints = %#v, want [cp33] (order guard)", repo.insertedCheckpointIDs)
	}
}

func TestProcessRespectsRaceFilter(t *testing.T) {
	repo := &fakeProcessorRepository{
		member:      Member{ID: "m5", RaceID: "r9"},
		memberFound: true,
		passed:      map[string]bool{},
		checkpoints: []Checkpoint{{ID: "cp33", Sort: 3, Type: domain.CheckpointFinish}},
	}

	logEntry := domain.RfidLog{
		ID:      "rfid-log-other-race",
		EventID: "ev1",
		Number:  5858,
		TimeMs:  5000,
		Board:   "board-1",
	}

	if err := newTestProcessor(repo).Process(context.Background(), logEntry, "r-other"); err != nil {
		t.Fatalf("Process() error = %v", err)
	}

	if repo.txCalls != 1 || len(repo.commitDecisions) != 1 || repo.commitDecisions[0] {
		t.Fatalf("transaction = calls:%d commits:%v, want one rolled-back read transition", repo.txCalls, repo.commitDecisions)
	}
}

func TestProcessSkipsLogWithoutParticipantKey(t *testing.T) {
	repo := &fakeProcessorRepository{memberFound: true}

	logEntry := domain.RfidLog{ID: "no-key", EventID: "ev1", TimeMs: 5000, Board: "board-1"}

	if err := newTestProcessor(repo).Process(context.Background(), logEntry, ""); err != nil {
		t.Fatalf("Process() error = %v", err)
	}
	if repo.memberCalls != 0 {
		t.Fatalf("member loads = %d, want 0 (no number, no epc)", repo.memberCalls)
	}
}
