package service

import (
	"context"
	"fmt"
	"log"

	"gitlab.com/fightmaster1/chrono-desk/internal/domain"
	"gitlab.com/fightmaster1/chrono-desk/internal/infrastructure/sqlite"
	"gitlab.com/fightmaster1/chrono-desk/internal/processor"
	"gitlab.com/fightmaster1/rfid-core/ingest"
	"gitlab.com/fightmaster1/rfid-core/telemetry"
)

type EdgeLiveStatus struct {
	Running    bool                         `json:"running"`
	Port       string                       `json:"port"`
	Received   int64                        `json:"received"`
	Inserted   int64                        `json:"inserted"`
	Duplicates int64                        `json:"duplicates"`
	Errors     int64                        `json:"errors"`
	LastError  string                       `json:"last_error"`
	Listeners  []telemetry.ListenerSnapshot `json:"listeners"`
}

func edgeSessionStatus(s *liveSession) EdgeLiveStatus {
	if s == nil {
		return EdgeLiveStatus{}
	}
	return EdgeLiveStatus{Running: !s.isFinished(), Port: s.port, Received: s.stats.Received.Load(), Inserted: s.stats.Inserted.Load(), Duplicates: s.stats.Duplicates.Load(), Errors: s.stats.Errors.Load(), LastError: s.lastError(), Listeners: s.metrics.Snapshot()}
}

func (m *LiveManager) StartEdge(store *sqlite.Store, eventID, port string) error {
	return m.startListener(store, eventID, port, true)
}

func (m *LiveManager) StopEdge(eventID string) {
	m.mu.Lock()
	s := m.edgeSessions[eventID]
	m.mu.Unlock()
	stopLiveSessions([]*liveSession{s})
}

func (m *LiveManager) ConfigureEdge(ctx context.Context, store *sqlite.Store, eventID string, bindings []domain.EdgeBinding) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return fmt.Errorf("приёмники приложения уже остановлены")
	}
	if s := m.edgeSessions[eventID]; s != nil && !s.isFinished() {
		return fmt.Errorf("остановите edge-приём перед изменением привязок")
	}
	return store.SetEdgeBindings(ctx, eventID, bindings)
}

type edgePublisher struct {
	store   *sqlite.Store
	eventID string
	stats   *LiveStats
	logger  *log.Logger
	onError func(error)
}

func (p *edgePublisher) Publish(ctx context.Context, event ingest.Event) error {
	p.stats.Received.Add(1)
	p.stats.LastReadMs.Store(event.Time)
	var accepted sqlite.EdgeAcceptance
	err := p.store.WithinTx(ctx, func(tx *sqlite.Store) error {
		var err error
		accepted, err = tx.AcceptEdgeObservation(ctx, p.eventID, event)
		if err != nil {
			return err
		}
		if !accepted.Inserted {
			return nil
		}
		// Join the event transaction: a lost process cannot leave accepted input
		// without its initial projection. Sidecar retains unacknowledged input.
		return processor.New(sqlite.NewProcessorRepo(tx), p.logger, false).Process(ctx, accepted.Log, "")
	})
	if err != nil {
		p.stats.Errors.Add(1)
		if p.onError != nil {
			p.onError(err)
		}
		return err
	}
	if accepted.Inserted {
		p.stats.Inserted.Add(1)
	} else {
		p.stats.Duplicates.Add(1)
	}
	return nil
}
