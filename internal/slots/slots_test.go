package slots

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

var (
	alice = Holder{AgentID: "a1", Name: "alice"}
	bob   = Holder{AgentID: "b1", Name: "bob"}
	owner = Holder{}
)

func newM(t *testing.T, alive func(int) bool) (*Manager, *clock) {
	t.Helper()
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	return New(Options{Sizes: map[string]int{"heavy": 2, "browser": 1}, Alive: alive, Now: c.now}), c
}

func TestQueueOrderAndRelease(t *testing.T) {
	m, _ := newM(t, nil)
	a, _ := m.Acquire("heavy", alice, "go test ./...", 0)
	b, _ := m.Acquire("heavy", bob, "pnpm build", 0)
	c, _ := m.Acquire("heavy", owner, "make", 0)
	d, _ := m.Acquire("heavy", bob, "cargo build", 0)
	if a.State != Held || b.State != Held || c.State != Waiting || c.Position != 1 || d.Position != 2 {
		t.Fatalf("states: %+v %+v %+v %+v", a, b, c, d)
	}
	if c.Holder != "owner" || a.Holder != "alice" {
		t.Fatalf("holders %q %q", c.Holder, a.Holder)
	}
	if err := m.Release(a.ID, bob); !errors.Is(err, ErrForbidden) {
		t.Fatalf("bob released alice's lease: %v", err)
	}
	if err := m.Release(a.ID, alice); err != nil {
		t.Fatal(err)
	}
	got, err := m.Wait(context.Background(), c.ID, owner, time.Second)
	if err != nil || got.State != Held {
		t.Fatalf("owner's lease after release: %+v %v", got, err)
	}
	if l, _ := m.Renew(d.ID, bob); l.Position != 1 {
		t.Fatalf("d position %d, want 1", l.Position)
	}
	v := m.View()
	if len(v.Kinds) != 2 || v.Kinds[1].Kind != "heavy" || v.Kinds[1].Held != 2 || v.Kinds[1].Waiting != 1 {
		t.Fatalf("view kinds %+v", v.Kinds)
	}
	if v.Leases[len(v.Leases)-1].ID != d.ID {
		t.Fatal("waiting leases come last")
	}
	if _, err := m.Acquire("gpu", alice, "x", 0); !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("unknown kind: %v", err)
	}
}

func TestWaitWakesOnGrant(t *testing.T) {
	m, _ := newM(t, nil)
	a, _ := m.Acquire("browser", alice, "playwright", 0)
	b, _ := m.Acquire("browser", bob, "playwright", 0)
	done := make(chan Lease)
	go func() {
		l, _ := m.Wait(context.Background(), b.ID, bob, 5*time.Second)
		done <- l
	}()
	time.Sleep(20 * time.Millisecond)
	_ = m.Release(a.ID, alice)
	select {
	case l := <-done:
		if l.State != Held {
			t.Fatalf("woke with %+v", l)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not wake on grant")
	}
}

func TestLapsedLeasesAreSwept(t *testing.T) {
	dead := map[int]bool{}
	var mu sync.Mutex
	m, c := newM(t, func(pid int) bool { mu.Lock(); defer mu.Unlock(); return !dead[pid] })
	a, _ := m.Acquire("heavy", alice, "a", 101)
	b, _ := m.Acquire("heavy", bob, "b", 102)
	w, _ := m.Acquire("heavy", owner, "w", 103)
	mu.Lock()
	dead[101] = true // alice's client crashed
	mu.Unlock()
	m.Sweep()
	if _, err := m.Renew(a.ID, alice); !errors.Is(err, ErrNotFound) {
		t.Fatalf("dead client's lease survived: %v", err)
	}
	if l, _ := m.Renew(w.ID, owner); l.State != Held {
		t.Fatalf("waiter not promoted: %+v", l)
	}
	c.add(TTL + time.Second) // bob stopped renewing
	_, _ = m.Renew(w.ID, owner)
	m.Sweep()
	if _, err := m.Renew(b.ID, bob); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired lease survived: %v", err)
	}
	if _, err := m.Renew(w.ID, owner); err != nil {
		t.Fatalf("renewed lease was swept: %v", err)
	}
}

func TestShrinkAndGrow(t *testing.T) {
	m, _ := newM(t, nil)
	_, _ = m.Acquire("browser", alice, "x", 0)
	b, _ := m.Acquire("browser", bob, "y", 0)
	m.SetSizes(map[string]int{"browser": 2, "heavy": 2})
	if l, _ := m.Renew(b.ID, bob); l.State != Held {
		t.Fatal("growing a kind promotes its waiters")
	}
	for range MaxPerHolder - 1 {
		if _, err := m.Acquire("heavy", alice, "z", 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Acquire("heavy", alice, "z", 0); !errors.Is(err, ErrTooMany) {
		t.Fatalf("per-holder bound: %v", err)
	}
}
