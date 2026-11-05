package fleet

import (
	"slices"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/discover"
)

func TestAdoptRefusesForeignAgents(t *testing.T) {
	for _, caller := range []Caller{OwnerCaller, {}, {AgentID: "worker"}} {
		for _, confirmed := range []bool{false, true} {
			touched := false
			f := &Fleet{opts: Options{Discover: &discover.Scanner{
				List:      func() ([]discover.Proc, error) { touched = true; return nil, nil },
				Terminate: func(int) error { touched = true; return nil },
			}}}
			req := AdoptRequest{PID: 123, Name: "existing", ConversationID: "known-session", Confirm: confirmed}
			for i := 0; i < 5; i++ {
				if _, err := f.Discover(caller); KindOf(err) != KindForbidden {
					t.Fatalf("discover: %v", err)
				}
				if _, err := f.PlanAdopt(caller, req); KindOf(err) != KindForbidden {
					t.Fatalf("plan: %v", err)
				}
				if _, err := f.Adopt(caller, req); KindOf(err) != KindForbidden {
					t.Fatalf("adopt: %v", err)
				}
			}
			if touched {
				t.Fatal("outside process table touched")
			}
			if len(f.agents) != 0 {
				t.Fatal("refusal registered a recoverable agent")
			}
		}
	}
}

func TestAdoptRefusalsLeaveManagedRecoveryUntouched(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	a := mustAgent(t, f, "head")
	f.mu.Lock()
	r := f.agents[a.ID]
	r.recovery = crashRecovery{attempts: []time.Time{time.Now()}, generation: 7, pending: true}
	before := r.recovery
	before.attempts = slices.Clone(before.attempts)
	count := len(f.agents)
	f.mu.Unlock()
	started := len(e.chat.Procs())
	for i := 0; i < 10; i++ {
		req := AdoptRequest{PID: a.PID, Name: a.Name, Confirm: true, ConversationID: a.SessionID}
		if _, err := f.Discover(OwnerCaller); KindOf(err) != KindForbidden {
			t.Fatal(err)
		}
		if _, err := f.PlanAdopt(OwnerCaller, req); KindOf(err) != KindForbidden {
			t.Fatal(err)
		}
		if _, err := f.Adopt(OwnerCaller, req); KindOf(err) != KindForbidden {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	after := r.recovery
	if !slices.Equal(before.attempts, after.attempts) || before.generation != after.generation || before.pending != after.pending || before.cancelWait != after.cancelWait {
		t.Fatal("refused adoption changed managed recovery")
	}
	if len(f.agents) != count || len(e.chat.Procs()) != started || len(e.term.Procs()) != 0 {
		t.Fatal("refused adoption registered or started an agent")
	}
}
