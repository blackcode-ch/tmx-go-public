package people_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/people"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

func open(t *testing.T, path string) *store.Store {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// A reminder set before a restart fires after it, once - including one whose time passed
// while the daemon was down.
func TestReminderFiresAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tmx.db")
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

	st := open(t, path)
	for _, r := range []model.Reminder{
		{ID: "past", FireAt: now.Add(-time.Hour).UnixMilli(), Target: "a1", Text: "overdue while down", State: "pending"},
		{ID: "soon", FireAt: now.Add(time.Minute).UnixMilli(), Target: "a1", Text: "later", State: "pending"},
	} {
		if err := st.AddReminder(r); err != nil {
			t.Fatal(err)
		}
	}
	st.Close() // the daemon stops

	st = open(t, path) // ... and starts again
	defer st.Close()
	var got []string
	s := &people.Scheduler{St: st, Now: func() time.Time { return now },
		Deliver: func(r model.Reminder) error { got = append(got, r.ID); return nil }}
	if n, err := s.Tick(); err != nil || n != 1 || len(got) != 1 || got[0] != "past" {
		t.Fatalf("catch-up: n=%d err=%v got=%v", n, err, got)
	}
	if n, _ := s.Tick(); n != 0 || len(got) != 1 {
		t.Fatalf("fired twice: %v", got)
	}
	now = now.Add(2 * time.Minute)
	if n, _ := s.Tick(); n != 1 || got[1] != "soon" {
		t.Fatalf("later: %v", got)
	}
	r, _ := st.GetReminder("soon")
	if r.State != "fired" || r.FiredAt == 0 {
		t.Fatalf("not marked: %+v", r)
	}
}

// A daemon that delivered but died before marking: the next start sees the delivery and only marks.
func TestReminderNotDoubledAfterCrashBetweenDeliverAndMark(t *testing.T) {
	st := open(t, filepath.Join(t.TempDir(), "tmx.db"))
	defer st.Close()
	st.AddReminder(model.Reminder{ID: "x", FireAt: 1, Target: "a1", Text: "t", State: "pending"})
	delivered := map[string]int{"x": 1} // the earlier daemon's delivery
	s := &people.Scheduler{St: st, Deliver: func(r model.Reminder) error {
		if delivered[r.ID] > 0 {
			return people.ErrAlreadyDelivered
		}
		delivered[r.ID]++
		return nil
	}}
	if n, err := s.Tick(); err != nil || n != 1 || delivered["x"] != 1 {
		t.Fatalf("n=%d err=%v delivered=%v", n, err, delivered)
	}
}

func TestReminderRetriesAfterFailedDelivery(t *testing.T) {
	st := open(t, filepath.Join(t.TempDir(), "tmx.db"))
	defer st.Close()
	now := time.UnixMilli(10_000)
	st.AddReminder(model.Reminder{ID: "x", FireAt: 1, Target: "a1", Text: "t", State: "pending"})
	fail := true
	s := &people.Scheduler{St: st, Now: func() time.Time { return now }, RetryAfter: time.Minute,
		Deliver: func(model.Reminder) error {
			if fail {
				return errors.New("agent busy")
			}
			return nil
		}}
	if n, _ := s.Tick(); n != 0 {
		t.Fatal("fired on failure")
	}
	r, _ := st.GetReminder("x")
	if r.State != "pending" || r.Attempts != 1 || r.LastError == "" || r.FireAt != now.Add(time.Minute).UnixMilli() {
		t.Fatalf("%+v", r)
	}
	fail, now = false, now.Add(time.Minute)
	if n, _ := s.Tick(); n != 1 {
		t.Fatal("retry did not fire")
	}
}

// Run wakes for a reminder added while it sleeps and stops with its context.
func TestRunFiresOnTime(t *testing.T) {
	st := open(t, filepath.Join(t.TempDir(), "tmx.db"))
	defer st.Close()
	var mu sync.Mutex
	fired := make(chan string, 1)
	s := &people.Scheduler{St: st, MaxSleep: time.Hour, Deliver: func(r model.Reminder) error {
		mu.Lock()
		defer mu.Unlock()
		fired <- r.ID
		return nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	st.AddReminder(model.Reminder{ID: "y", FireAt: time.Now().Add(200 * time.Millisecond).UnixMilli(), Target: "a1", Text: "t", State: "pending"})
	s.Wake()
	select {
	case id := <-fired:
		if id != "y" {
			t.Fatal(id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not fire")
	}
	cancel()
	<-done
}

func TestRules(t *testing.T) {
	if p, err := people.Phone("+41 79 123 45 67"); err != nil || p != "41791234567" {
		t.Fatal(p, err)
	}
	for _, bad := range []string{"x@y.ch?bcc=z@w.com", "a b@c.d", "@x", "nope"} {
		if _, err := people.Email(bad); err == nil {
			t.Fatal("accepted", bad)
		}
	}
	if _, err := people.Due("2026-02-30"); err == nil {
		t.Fatal("bad date accepted")
	}
	if k, err := people.NormKind(""); err != nil || k != "team" {
		t.Fatal(k)
	}
	if _, err := people.NormKind("vip"); err == nil {
		t.Fatal("bad kind")
	}
	at, _ := people.DueFireAt("2026-11-09", time.UTC)
	if at.Hour() != 9 || at.Day() != 9 {
		t.Fatal(at)
	}
}
