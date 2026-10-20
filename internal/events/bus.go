// Package events is the in-process pub/sub the reconciler, drivers and SSE
// handler share. Delivery is best-effort: a slow subscriber loses the oldest
// events rather than blocking publishers, because a UI tab that fell behind
// must never stall the control plane.
package events

import (
	"context"
	"sync"
	"time"
)

// Type names an event. Kept as strings so the SSE "event:" field is the type.
type Type string

const (
	ApplyStarted    Type = "apply.started"
	ApplyFinished   Type = "apply.finished"
	ApplyFailed     Type = "apply.failed"
	ComponentHealth Type = "component.health"
	StateChanged    Type = "state.changed" // generation bumped by a write
	Heartbeat       Type = "heartbeat"
)

// Event is one message on the bus.
type Event struct {
	Type Type      `json:"type"`
	At   time.Time `json:"at"`
	Data any       `json:"data,omitempty"`
}

// Bus fans out events to subscribers.
type Bus struct {
	mu   sync.RWMutex
	subs map[*subscriber]struct{}
	buf  int
}

type subscriber struct {
	ch      chan Event
	dropped int
}

// New creates a bus whose subscribers each buffer up to buf events.
func New(buf int) *Bus {
	if buf <= 0 {
		buf = 64
	}
	return &Bus{subs: map[*subscriber]struct{}{}, buf: buf}
}

// Publish delivers e to every subscriber without blocking.
func (b *Bus) Publish(t Type, data any) {
	e := Event{Type: t, At: time.Now().UTC(), Data: data}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		select {
		case s.ch <- e:
		default:
			// Drop the oldest to make room for the newest.
			select {
			case <-s.ch:
				s.dropped++
			default:
			}
			select {
			case s.ch <- e:
			default:
			}
		}
	}
}

// Subscribe returns a channel that receives events until ctx is done.
func (b *Bus) Subscribe(ctx context.Context) <-chan Event {
	s := &subscriber{ch: make(chan Event, b.buf)}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	go func() {
		<-ctx.Done()
		b.mu.Lock()
		delete(b.subs, s)
		b.mu.Unlock()
		close(s.ch)
	}()
	return s.ch
}

// Subscribers reports how many listeners are attached (for /state and tests).
func (b *Bus) Subscribers() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}
