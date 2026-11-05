package fleet

import (
	"sync"
	"sync/atomic"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// subBuffer is each subscriber's queue length; when it is full, events are dropped and the
// subscriber is flagged for a resync (it should fetch a fresh snapshot).
const subBuffer = 512

// Bus fans BusEvents out to subscribers without ever blocking the publisher.
type Bus struct {
	mu   sync.Mutex
	subs map[*Sub]struct{}
}

// Sub is one subscription.
type Sub struct {
	ch     chan model.BusEvent
	resync atomic.Bool
}

// C delivers events in publish order (with gaps after a drop; see TakeResync).
func (s *Sub) C() <-chan model.BusEvent { return s.ch }

// TakeResync reports (and clears) whether events were dropped since the last call.
func (s *Sub) TakeResync() bool { return s.resync.Swap(false) }

func newBus() *Bus { return &Bus{subs: map[*Sub]struct{}{}} }

func (b *Bus) Subscribe() *Sub {
	s := &Sub{ch: make(chan model.BusEvent, subBuffer)}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

// Unsubscribe removes s; its channel is not closed (the reader just stops reading).
func (b *Bus) Unsubscribe(s *Sub) {
	b.mu.Lock()
	delete(b.subs, s)
	b.mu.Unlock()
}

func (b *Bus) Publish(ev model.BusEvent) {
	if ev.At == 0 {
		ev.At = model.Now()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for s := range b.subs {
		select {
		case s.ch <- ev:
		default:
			s.resync.Store(true)
		}
	}
}
