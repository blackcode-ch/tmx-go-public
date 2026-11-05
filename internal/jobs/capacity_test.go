package jobs

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestFinishedHistoryCannotLockOutOwner(t *testing.T) {
	m := manager(t, Options{})
	m.mu.Lock()
	for n := 0; n < 1000; n++ {
		i := Identity{"owner", "owner", fmt.Sprint(n)}
		r := Record{ID: i.ID(), Identity: i, State: "completed", Heartbeat: time.Now()}
		m.entries[r.ID] = &entry{record: r}
	}
	m.mu.Unlock()
	for n := 1000; n < 1003; n++ {
		r, err := m.SubmitAudit(Identity{"owner", "owner", fmt.Sprint(n)}, true, 3, bytesReader("abc"))
		if err != nil {
			t.Fatal("finished history blocked owner", err)
		}
		wait(t, m, r.ID, "completed")
	}
	if len(m.List("owner", true)) > 1000 {
		t.Fatal("history grew without bound")
	}
}

func TestBackgroundHistoryPreservesForegroundReserve(t *testing.T) {
	m := manager(t, Options{MaxJobs: 10})
	m.mu.Lock()
	for n := 0; n < 9; n++ {
		i := Identity{"peer", "peer", fmt.Sprint(n)}
		r := Record{ID: i.ID(), Identity: i, State: "quarantined"}
		m.entries[r.ID] = &entry{record: r}
	}
	m.mu.Unlock()
	if _, err := m.SubmitAudit(Identity{"agent", "agent", "more"}, false, 3, bytesReader("abc")); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	r, err := m.SubmitAudit(Identity{"owner", "owner", "reserved"}, true, 3, bytesReader("abc"))
	if err != nil {
		t.Fatal(err)
	}
	wait(t, m, r.ID, "completed")
}

func TestPeerBudgetAndPrincipalContract(t *testing.T) {
	m := manager(t, Options{})
	m.mu.Lock()
	for n := 0; n < 100; n++ {
		peer := fmt.Sprint(n / 10)
		i := Identity{"chat-peer:" + peer, "owner", fmt.Sprint(n)}
		r := Record{ID: i.ID(), Identity: i, State: "queued", Peer: peer, Chat: fmt.Sprint(n)}
		m.entries[r.ID] = &entry{record: r}
	}
	m.mu.Unlock()
	_, err := m.SubmitTask(Identity{"chat-peer:0", "owner", "extra"}, false, 3, AuditTask{Peer: "0", Chat: "new", Open: bytesReader("abc")})
	if !errors.Is(err, ErrBusy) {
		t.Fatal("peer exceeded ten", err)
	}
	r, err := m.SubmitAudit(Identity{"owner", "owner", "owner-free"}, true, 3, bytesReader("abc"))
	if err != nil {
		t.Fatal("peers consumed owner budget", err)
	}
	wait(t, m, r.ID, "completed")
	for _, i := range []Identity{{"agent", "owner", "spoof"}, {"agent", "agent", "promote"}} {
		if _, err := m.SubmitAudit(i, true, 3, bytesReader("abc")); !errors.Is(err, ErrForbidden) {
			t.Fatal("foreground spoof", err)
		}
	}
	if _, err := m.SubmitAudit(Identity{"agent", "owner", "spoof-owner"}, false, 3, bytesReader("abc")); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
}
