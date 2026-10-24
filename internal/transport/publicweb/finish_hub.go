package publicweb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"gitlab.com/fightmaster1/chrono-desk/internal/finishchange"
)

var errFinishStreamBusy = errors.New("слишком много подключений к трансляции")

const maxFinishClients = 32
const maxFinishFrameBytes = 256 * 1024

type finishFrame struct {
	id   string
	json []byte
	wire []byte
}

// finishHub owns the single commit dispatcher for one publication epoch. Writers
// never acquire its locks: they update only finishchange.Signal.
type finishHub struct {
	ctx           context.Context
	cancel        context.CancelFunc
	done          chan struct{}
	signal        *finishchange.Signal
	load          func(context.Context, string) (publicFinishes, error)
	key           func(string) string
	eventID       string
	epoch         uint64
	buildSlots    chan struct{}
	mu            sync.Mutex
	groups        map[string]*finishGroup
	clients       int
	stopped       bool
	workers       sync.WaitGroup
	activeWorkers atomic.Int64
	builds        atomic.Uint64
	frameSequence atomic.Uint64
}

type finishGroup struct {
	hub         *finishHub
	race        string
	ctx         context.Context
	cancel      context.CancelFunc
	wake        chan struct{}
	clients     map[*finishSubscription]struct{} // protected by hub.mu
	buildSerial uint64                           // protected by hub.mu
}

type finishSubscription struct {
	group    *finishGroup
	ctx      context.Context
	cancel   context.CancelFunc
	wake     chan struct{}
	mu       sync.Mutex
	pending  *finishFrame
	lastID   string
	err      error
	minBuild uint64
	closed   sync.Once
}

func newFinishHub(eventID string, epoch uint64, signal *finishchange.Signal, load func(context.Context, string) (publicFinishes, error), key func(string) string) *finishHub {
	ctx, cancel := context.WithCancel(context.Background())
	h := &finishHub{ctx: ctx, cancel: cancel, done: make(chan struct{}), signal: signal, load: load, key: key, eventID: eventID, epoch: epoch, buildSlots: make(chan struct{}, 4), groups: make(map[string]*finishGroup)}
	select {
	case <-signal.Wake():
	default:
	}
	go h.run()
	return h
}

func (h *finishHub) run() {
	defer close(h.done)
	defer func() {
		h.mu.Lock()
		h.stopped = true
		h.cancel()
		h.mu.Unlock()
		h.workers.Wait()
	}()
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-h.signal.Done():
			return
		case <-h.signal.Wake():
			h.mu.Lock()
			for _, g := range h.groups {
				g.request()
			}
			h.mu.Unlock()
		}
	}
}

func (h *finishHub) subscribe(race string) (*finishSubscription, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopped || h.ctx.Err() != nil {
		return nil, context.Canceled
	}
	if h.clients >= maxFinishClients {
		return nil, errFinishStreamBusy
	}
	g := h.groups[race]
	if g == nil || g.ctx.Err() != nil {
		if h.activeWorkers.Load() >= maxFinishClients {
			return nil, errFinishStreamBusy
		}
		ctx, cancel := context.WithCancel(h.ctx)
		g = &finishGroup{hub: h, race: race, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), clients: make(map[*finishSubscription]struct{})}
		h.groups[race] = g
		h.workers.Add(1)
		h.activeWorkers.Add(1)
		go func() { defer h.workers.Done(); defer h.activeWorkers.Add(-1); g.run() }()
	}
	ctx, cancel := context.WithCancel(g.ctx)
	sub := &finishSubscription{group: g, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1)}
	sub.minBuild = g.buildSerial + 1
	g.clients[sub] = struct{}{}
	h.clients++
	// Always reread on subscribe/reconnect, even with an unchanged generation.
	g.request()
	return sub, nil
}

func (g *finishGroup) request() {
	select {
	case g.wake <- struct{}{}:
	default:
	}
}

func (g *finishGroup) run() {
	var previous *finishFrame
	for {
		select {
		case <-g.ctx.Done():
			return
		case <-g.wake:
		}
		g.hub.mu.Lock()
		g.buildSerial++
		buildSerial := g.buildSerial
		g.hub.mu.Unlock()
		generation := g.hub.signal.Generation()
		select {
		case g.hub.buildSlots <- struct{}{}:
		case <-g.ctx.Done():
			return
		}
		ctx, cancel := context.WithTimeout(g.ctx, time.Second)
		g.hub.builds.Add(1)
		payload, err := g.hub.load(ctx, g.race)
		cancel()
		<-g.hub.buildSlots
		var data []byte
		if err == nil {
			data, err = json.Marshal(payload)
		}
		if err == nil && len(data) > maxFinishFrameBytes {
			err = errors.New("снимок финишей превышает размер трансляции")
		}
		if err != nil {
			g.hub.mu.Lock()
			for sub := range g.clients {
				sub.fail(err)
			}
			g.cancel()
			g.hub.mu.Unlock()
			return
		}
		if previous == nil || !bytes.Equal(previous.json, data) {
			revision := g.hub.frameSequence.Add(1)
			id := g.hub.key(fmt.Sprintf("view:%d:%s:%s:%d", g.hub.epoch, g.hub.eventID, g.race, revision))
			wire := append([]byte("event: snapshot\nid: "+id+"\ndata: "), data...)
			wire = append(wire, '\n', '\n')
			previous = &finishFrame{id: id, json: data, wire: wire}
		}
		g.hub.mu.Lock()
		if g.ctx.Err() == nil {
			for sub := range g.clients {
				if buildSerial >= sub.minBuild {
					sub.offer(previous)
				}
			}
		}
		g.hub.mu.Unlock()
		// A commit while the snapshot was being built must not be swallowed.
		// Publish completed states even under continuous ingress, then rebuild.
		if g.hub.signal.Generation() != generation {
			g.request()
		}
	}
}

func (s *finishSubscription) offer(frame *finishFrame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil || s.lastID == frame.id {
		return
	}
	s.pending = frame
	s.lastID = frame.id
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *finishSubscription) take() *finishFrame {
	s.mu.Lock()
	defer s.mu.Unlock()
	frame := s.pending
	s.pending = nil
	return frame
}

func (s *finishSubscription) fail(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
	s.cancel()
}

func (s *finishSubscription) failure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	return s.ctx.Err()
}

func (s *finishSubscription) close() {
	s.closed.Do(func() {
		s.cancel()
		h := s.group.hub
		h.mu.Lock()
		delete(s.group.clients, s)
		h.clients--
		if len(s.group.clients) == 0 {
			s.group.cancel()
			if h.groups[s.group.race] == s.group {
				delete(h.groups, s.group.race)
			}
		}
		h.mu.Unlock()
		s.mu.Lock()
		s.pending = nil
		s.mu.Unlock()
	})
}
