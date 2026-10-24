// Package finishchange connects committed projection changes to disposable views.
// A Signal has one dispatcher consumer; client fan-out belongs to that dispatcher.
package finishchange

import (
	"sync"
	"sync/atomic"
)

// Signal retains a generation and at most one wake-up, never finish history.
// Notify does not query, invoke callbacks, enumerate clients or start goroutines.
type Signal struct {
	generation atomic.Uint64
	wake       chan struct{}
	done       chan struct{}
	closed     sync.Once
}

func New() *Signal { return &Signal{wake: make(chan struct{}, 1), done: make(chan struct{})} }

func (s *Signal) Generation() uint64    { return s.generation.Load() }
func (s *Signal) Wake() <-chan struct{} { return s.wake }
func (s *Signal) Done() <-chan struct{} { return s.done }

func (s *Signal) Notify() {
	s.generation.Add(1)
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Close cancels observers. The wake channel is deliberately never closed:
// an already committing writer can safely notify concurrently with shutdown.
func (s *Signal) Close() { s.closed.Do(func() { close(s.done) }) }
