package jobs

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func manager(t *testing.T, o Options) *Manager {
	t.Helper()
	if o.Path == "" {
		o.Path = filepath.Join(t.TempDir(), "jobs.json")
	}
	if o.Fresh == nil {
		o.Fresh = func() bool { return true }
	}
	m, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		m.Close()
		// Close cancels workers; Done also waits for any in-flight ledger sync.
		// Let the test runner's timeout bound I/O rather than assuming disk speed.
		<-m.Done()
	})
	return m
}
func wait(t *testing.T, m *Manager, id, state string) Record {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r, err := m.Get(id, "", true)
		if err != nil {
			t.Fatal(err)
		}
		if r.State == state {
			// Cached terminal state is published before its ledger sync finishes.
			// Join that transaction before the next nonblocking Submit/Release.
			m.mutation.Lock()
			m.mutation.Unlock()
			return r
		}
		time.Sleep(time.Millisecond)
	}
	r, _ := m.Get(id, "", true)
	t.Fatalf("wanted %s, got %+v", state, r)
	return Record{}
}
func bytesReader(s string) func(context.Context) (io.ReadCloser, error) {
	return func(context.Context) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(s)), nil }
}

func TestQuarantineIdentityAndRelease(t *testing.T) {
	m := manager(t, Options{Failures: 2})
	a := Identity{"agent-a", "agent-a", "same-audit"}
	b := Identity{"agent-b", "agent-b", "same-audit"}
	fail := func(context.Context) (io.ReadCloser, error) { return nil, errors.New("same error") }
	for n := 0; n < 2; n++ {
		r, err := m.SubmitAudit(a, false, 0, fail)
		if err != nil {
			t.Fatal(err)
		}
		state := "failed"
		if n == 1 {
			state = "quarantined"
		}
		wait(t, m, r.ID, state)
	}
	r, _ := m.Get(a.ID(), "agent-a", false)
	if r.Card || r.Fingerprint == "" {
		t.Fatalf("missing owner card/fingerprint: %+v", r)
	}
	if _, err := m.Release(a.ID(), "agent-b", false); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := m.SubmitAudit(a, false, 0, fail); !errors.Is(err, ErrQuarantined) {
		t.Fatal(err)
	}
	rb, err := m.SubmitAudit(b, false, 3, bytesReader("abc"))
	if err != nil {
		t.Fatal(err)
	}
	wait(t, m, rb.ID, "completed")
	if _, err := m.Release(a.ID(), "agent-a", false); err != nil {
		t.Fatal(err)
	}
	r, _ = m.Get(a.ID(), "", true)
	if r.State != "released" || r.Card {
		t.Fatal(r)
	}
	r, err = m.SubmitAudit(a, false, 3, bytesReader("abc"))
	if err != nil {
		t.Fatal(err)
	}
	r = wait(t, m, r.ID, "completed")
	if r.Digest != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatal(r.Digest)
	}
}

func TestRestartQuarantinesWithoutOpening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	m := manager(t, Options{Path: path})
	entered := make(chan struct{})
	unblock := make(chan struct{})
	r, err := m.SubmitAudit(Identity{"owner", "owner", "audit"}, true, 0, func(ctx context.Context) (io.ReadCloser, error) { close(entered); <-unblock; return nil, ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	// Copy the crash-time durable state, not a graceful shutdown snapshot.
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(t.TempDir(), "jobs.json")
	if err = os.WriteFile(copyPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	m.Close()
	close(unblock)
	<-m.Done()
	next := manager(t, Options{Path: copyPath})
	q := wait(t, next, r.ID, "quarantined")
	if !q.Card {
		t.Fatal("owner card missing")
	}
	var opens atomic.Int32
	_, err = next.SubmitAudit(q.Identity, true, 0, func(context.Context) (io.ReadCloser, error) { opens.Add(1); return nil, nil })
	if !errors.Is(err, ErrQuarantined) || opens.Load() != 0 {
		t.Fatal("poison job restarted", err)
	}
}

func TestGovernorDiesDuringAuditCancelIndependent(t *testing.T) {
	var fresh atomic.Bool
	fresh.Store(true)
	m := manager(t, Options{Fresh: fresh.Load})
	entered := make(chan struct{})
	unblock := make(chan struct{})
	r, err := m.SubmitAudit(Identity{"owner", "owner", "blocked"}, true, 0, func(ctx context.Context) (io.ReadCloser, error) { close(entered); <-unblock; return nil, ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	fresh.Store(false)
	started := time.Now()
	v, err := m.Cancel(r.ID, "owner", true)
	if err != nil || v.State != "cancelling" || time.Since(started) > 100*time.Millisecond {
		t.Fatal("cancel blocked", v, err)
	}
	// Cancel just signalled m.dirty (line 408 in jobs.go), which wakes releaseWorker into a
	// blocking m.mutation.Lock()+persist()+Unlock() in the background - a real race against
	// this SubmitAudit's own non-blocking TryLock (jobs.go:313), genuinely busy on a slow CI
	// runner, not a test bug. ErrBusy's own contract is "retry with the same identity".
	deadline := time.Now().Add(time.Second)
	for {
		_, err = m.SubmitAudit(Identity{"agent", "agent", "bg"}, false, 3, bytesReader("abc"))
		if !errors.Is(err, ErrBusy) || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !errors.Is(err, ErrDegraded) {
		t.Fatal(err)
	}
	if !m.Degraded() {
		t.Fatal("missing degraded health")
	}
	close(unblock)
	wait(t, m, r.ID, "cancelled")
	fg, err := m.SubmitAudit(Identity{"owner", "owner", "fallback"}, true, 3, bytesReader("abc"))
	if err != nil {
		t.Fatal(err)
	}
	wait(t, m, fg.ID, "completed")
}

func TestBoundedCapacityAndPanic(t *testing.T) {
	m := manager(t, Options{MaxJobs: 1, Failures: 1})
	r, err := m.SubmitAudit(Identity{"a", "a", "panic"}, false, 0, func(context.Context) (io.ReadCloser, error) { panic("poison") })
	if err != nil {
		t.Fatal(err)
	}
	wait(t, m, r.ID, "quarantined")
	if _, err = m.SubmitAudit(Identity{"b", "b", "next"}, false, 0, bytesReader("")); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if got := m.List("b", false); len(got) != 0 {
		t.Fatal("identity leaked", got)
	}
}
