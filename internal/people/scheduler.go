// Package people holds the rules for People, Clients, Promises and the durable reminder
// scheduler (docs/PEOPLE_PROMISES.md). Nothing here logs personal data.
package people

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// Store is the part of the store the scheduler uses.
type Store interface {
	DueReminders(now int64) ([]model.Reminder, error)
	NextReminderAt() (int64, error)
	MarkReminderFired(id string, at int64) (bool, error)
	ReminderFailed(id, msg string, retryAt int64) error
}

// ErrAlreadyDelivered tells the scheduler the delivery for this reminder already exists (the
// daemon stopped after delivering but before marking): it is marked fired, never sent twice.
var ErrAlreadyDelivered = errors.New("reminder already delivered")

// Scheduler fires due reminders. A reminder is in the store before it is scheduled, so a
// restart loses nothing: Run fires everything overdue at once, then sleeps until the next one.
// Deliver must be idempotent per reminder ID (the fleet uses the reminder ID as the message ID).
type Scheduler struct {
	St      Store
	Deliver func(model.Reminder) error
	Now     func() time.Time
	// MaxSleep bounds one wait, so a reminder added without Wake is still seen soon.
	MaxSleep   time.Duration
	RetryAfter time.Duration
	OnError    func(id string, err error)

	once sync.Once
	wake chan struct{}
	mu   sync.Mutex // one Tick at a time
}

func (s *Scheduler) init() {
	s.once.Do(func() {
		if s.Now == nil {
			s.Now = time.Now
		}
		if s.MaxSleep == 0 {
			s.MaxSleep = 30 * time.Second
		}
		if s.RetryAfter == 0 {
			s.RetryAfter = time.Minute
		}
		s.wake = make(chan struct{}, 1)
	})
}

// Wake makes Run look again now (call after adding or changing a reminder).
func (s *Scheduler) Wake() {
	s.init()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Tick fires every due reminder once and returns how many fired.
func (s *Scheduler) Tick() (int, error) {
	s.init()
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now().UnixMilli()
	due, err := s.St.DueReminders(now)
	if err != nil {
		return 0, err
	}
	fired := 0
	for _, r := range due {
		if err := s.Deliver(r); err != nil && !errors.Is(err, ErrAlreadyDelivered) {
			if s.OnError != nil {
				s.OnError(r.ID, err)
			}
			if ferr := s.St.ReminderFailed(r.ID, err.Error(), now+s.RetryAfter.Milliseconds()); ferr != nil {
				return fired, ferr
			}
			continue
		}
		ok, err := s.St.MarkReminderFired(r.ID, now)
		if err != nil {
			return fired, err
		}
		if ok {
			fired++
		}
	}
	return fired, nil
}

// Run ticks until ctx ends.
func (s *Scheduler) Run(ctx context.Context) {
	s.init()
	for {
		if _, err := s.Tick(); err != nil && s.OnError != nil {
			s.OnError("", err)
		}
		wait := s.MaxSleep
		if next, err := s.St.NextReminderAt(); err == nil && next > 0 {
			if d := time.Duration(next-s.Now().UnixMilli()) * time.Millisecond; d < wait {
				wait = d
			}
		}
		if wait < 50*time.Millisecond {
			wait = 50 * time.Millisecond
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-s.wake:
			t.Stop()
		case <-t.C:
		}
	}
}
