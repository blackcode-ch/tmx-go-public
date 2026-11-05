package fleet

import (
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

func TestCrashRecoveryBudget(t *testing.T) {
	now := time.Now()
	w := crashRecovery{}
	for i, want := range []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute} {
		delay, reason := w.next(now)
		if delay != want || reason != "" {
			t.Fatalf("attempt %d: %s %q", i, delay, reason)
		}
		w.attempts = append(w.attempts, now)
	}
	if _, reason := w.next(now); !strings.Contains(reason, "crash loop") {
		t.Fatal(reason)
	}
	if _, reason := w.next(now.Add(16 * time.Minute)); !strings.Contains(reason, "within an hour") {
		t.Fatal(reason)
	}
	if delay, reason := w.next(now.Add(time.Hour)); delay != 30*time.Second || reason != "" {
		t.Fatalf("%s %q", delay, reason)
	}
}

func TestRecoveryExitLimitAndDeliberateReset(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	a := mustAgent(t, f, "head")
	f.mu.Lock()
	r := f.agents[a.ID]
	r.recovery.attempts = []time.Time{time.Now(), time.Now(), time.Now()}
	f.mu.Unlock()
	e.chat.Last(a.ID).Exit(1, nil)
	eventually(t, "crash loop stopped", func() bool { return mustAgent(t, f, "head").Desired == "stopped" })
	if got := mustAgent(t, f, "head"); !strings.Contains(got.Error, "crash loop") {
		t.Fatal(got.Error)
	}
	if _, err := f.StartAgent(OwnerCaller, a.ID); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(r.recovery.attempts) != 0 || r.recovery.pending {
		t.Fatal("deliberate start did not reset recovery")
	}
}

func TestRecoveryFencesStopAndNewStart(t *testing.T) {
	for _, action := range []string{"stop", "start", "remove"} {
		t.Run(action, func(t *testing.T) {
			e := newEnv(t)
			f := e.started()
			a, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "recover"})
			if err != nil {
				t.Fatal(err)
			}
			e.chat.Last(a.ID).Exit(1, nil)
			waitState(t, f, a.Name, model.StateError)
			f.mu.Lock()
			r := f.agents[a.ID]
			generation := r.recovery.generation
			pending := r.recovery.pending
			f.mu.Unlock()
			if !pending {
				t.Fatal("unexpected exit did not schedule recovery")
			}
			switch action {
			case "stop":
				_, err = f.StopAgent(OwnerCaller, a.ID)
			case "start":
				_, err = f.StartAgent(OwnerCaller, a.ID)
			case "remove":
				err = f.RemoveAgent(OwnerCaller, a.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := e.chat.Last(a.ID)
			err = f.startAgentOpts(a.ID, "", startOpts{recovery: r, recoveryGeneration: generation})
			if action != "remove" && err != nil {
				t.Fatal(err)
			}
			if e.chat.Last(a.ID) != before {
				t.Fatal("stale recovery started a process")
			}
		})
	}
}

func TestRecoveryUsesAdmissionAndCountsAttempt(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	a := mustAgent(t, f, "head")
	e.chat.Last(a.ID).Exit(1, nil)
	waitState(t, f, a.Name, model.StateError)
	f.mu.Lock()
	r := f.agents[a.ID]
	generation := r.recovery.generation
	f.mu.Unlock()
	if err := f.startAgentOpts(a.ID, "", startOpts{recovery: r, recoveryGeneration: generation}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(r.recovery.attempts) != 1 || r.recovery.pending || r.proc == nil {
		t.Fatal("recovery attempt not recorded/started")
	}
}

func TestIntentionalStopDoesNotRecover(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	a := mustAgent(t, f, "head")
	if _, err := f.StopAgent(OwnerCaller, a.ID); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.agents[a.ID]
	if r.recovery.pending || len(r.recovery.attempts) != 0 {
		t.Fatal("intentional stop scheduled recovery")
	}
}

func TestRecoveryAdmissionFailureStopsWithoutRetry(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	a := mustAgent(t, f, "head")
	before := e.chat.Last(a.ID)
	before.Exit(1, nil)
	waitState(t, f, a.Name, model.StateError)
	f.mu.Lock()
	r := f.agents[a.ID]
	generation := r.recovery.generation
	f.opts.Elevated = func() bool { return true }
	f.mu.Unlock()
	f.restoreAfterCrash(r, generation)
	got := mustAgent(t, f, a.Name)
	if got.Desired != "stopped" || !strings.Contains(got.Error, "automatic restart failed") {
		t.Fatalf("%+v", got)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(r.recovery.attempts) != 1 || r.recovery.pending || e.chat.Last(a.ID) != before {
		t.Fatal("failed admission retried or escaped counting")
	}
}

func TestRecoveryCancellationReleasesWaiter(t *testing.T) {
	w := crashRecovery{pending: true, cancelWait: make(chan struct{})}
	wait := w.cancelWait
	w.cancel(false)
	select {
	case <-wait:
	default:
		t.Fatal("waiter not cancelled")
	}
}
