package service

import (
	"context"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/processor"
	"gitlab.com/fightmaster1/chrono-desk/internal/ranking"
)

// ProtocolStore is the query port consumed by protocol rendering.
type ProtocolStore interface {
	GetRace(ctx context.Context, raceID string) (domain.Race, error)
	ListMembersByRace(ctx context.Context, raceID string) ([]domain.Member, error)
	ListCategories(ctx context.Context) (map[string]domain.Category, error)
	LastPassesInWindow(ctx context.Context, race domain.Race, members []domain.Member) (map[string]ranking.LastPass, error)
}

type editStore interface {
	WithinTx(ctx context.Context, fn func(editTxStore) error) error
}

type editTxStore interface {
	UpdateEntityField(ctx context.Context, table, field, id string, value any) (old any, err error)
	InsertLocalChange(ctx context.Context, c sqlite.LocalChange) error
	GetMember(ctx context.Context, memberID string) (domain.Member, error)
	GetRace(ctx context.Context, raceID string) (domain.Race, error)
	GetEvent(ctx context.Context, id string) (domain.Event, error)
	ListRaceCategories(ctx context.Context, raceID string) ([]domain.Category, error)
	ShiftMemberStarts(ctx context.Context, raceID string, deltaMs int64) ([]sqlite.MemberStartShift, error)
}

type editReplayStore interface {
	ListLocalChanges(ctx context.Context) ([]sqlite.LocalChange, error)
	DeleteCheckpointCascade(ctx context.Context, id string) error
	AttachRaceCategory(ctx context.Context, raceID, categoryID string) error
	DetachRaceCategory(ctx context.Context, raceID, categoryID string) error
	UpdateEntityField(ctx context.Context, table, field, id string, value any) (old any, err error)
}

type sqliteEditStore struct {
	store *sqlite.Store
}

func newSQLiteEditStore(store *sqlite.Store) editStore {
	return sqliteEditStore{store: store}
}

func (s sqliteEditStore) WithinTx(ctx context.Context, fn func(editTxStore) error) error {
	return s.store.WithinTx(ctx, func(txStore *sqlite.Store) error {
		return fn(txStore)
	})
}

type manualFinishStore interface {
	GetMember(ctx context.Context, memberID string) (domain.Member, error)
	GetRace(ctx context.Context, raceID string) (domain.Race, error)
	UpdateMemberFinish(ctx context.Context, memberID string, start *int64, finish int64, clean *string) error
}

type recountStore interface {
	WithinTx(ctx context.Context, fn func(recountTxStore) error) error
}

type recountTxStore interface {
	manualFinishStore
	WipeDerivedResults(ctx context.Context, eventID, raceID string) error
	ListRfidLogs(ctx context.Context, eventID string) ([]domain.RfidLog, error)
	ListManualResults(ctx context.Context, eventID, raceID string) ([]sqlite.ManualResult, error)
	ProcessorRepository() processor.Repository
}

type sqliteRecountStore struct {
	store *sqlite.Store
}

func newSQLiteRecountStore(store *sqlite.Store) recountStore {
	return sqliteRecountStore{store: store}
}

func (s sqliteRecountStore) WithinTx(ctx context.Context, fn func(recountTxStore) error) error {
	return s.store.WithinTx(ctx, func(txStore *sqlite.Store) error {
		return fn(sqliteRecountTxStore{store: txStore})
	})
}

type sqliteRecountTxStore struct {
	store *sqlite.Store
}

func (s sqliteRecountTxStore) GetMember(ctx context.Context, memberID string) (domain.Member, error) {
	return s.store.GetMember(ctx, memberID)
}

func (s sqliteRecountTxStore) GetRace(ctx context.Context, raceID string) (domain.Race, error) {
	return s.store.GetRace(ctx, raceID)
}

func (s sqliteRecountTxStore) UpdateMemberFinish(ctx context.Context, memberID string, start *int64, finish int64, clean *string) error {
	return s.store.UpdateMemberFinish(ctx, memberID, start, finish, clean)
}

func (s sqliteRecountTxStore) WipeDerivedResults(ctx context.Context, eventID, raceID string) error {
	return s.store.WipeDerivedResults(ctx, eventID, raceID)
}

func (s sqliteRecountTxStore) ListRfidLogs(ctx context.Context, eventID string) ([]domain.RfidLog, error) {
	return s.store.ListRfidLogs(ctx, eventID)
}

func (s sqliteRecountTxStore) ListManualResults(ctx context.Context, eventID, raceID string) ([]sqlite.ManualResult, error) {
	return s.store.ListManualResults(ctx, eventID, raceID)
}

func (s sqliteRecountTxStore) ProcessorRepository() processor.Repository {
	return sqlite.NewProcessorRepo(s.store)
}

type importStore interface {
	ApplyEventImport(ctx context.Context, d sqlite.EventImportData) error
	ReapplyLocalEdits(ctx context.Context) (int, error)
}

type sqliteImportStore struct {
	store *sqlite.Store
}

func newSQLiteImportStore(store *sqlite.Store) importStore {
	return sqliteImportStore{store: store}
}

func (s sqliteImportStore) ApplyEventImport(ctx context.Context, d sqlite.EventImportData) error {
	return s.store.ApplyEventImport(ctx, d)
}

func (s sqliteImportStore) ReapplyLocalEdits(ctx context.Context) (int, error) {
	return ReapplyLocalEdits(ctx, s.store)
}
