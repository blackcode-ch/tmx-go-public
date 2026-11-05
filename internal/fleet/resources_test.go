package fleet

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/sysinfo"
)

// fakeRes is resource control with a settable agent limit.
type fakeRes struct {
	mu      sync.Mutex
	max     int
	resumed []string // "id:reason:raise"
}

func (r *fakeRes) setMax(n int) { r.mu.Lock(); r.max = n; r.mu.Unlock() }

func (r *fakeRes) Admit(running int) (bool, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if running >= r.max {
		return false, "limit reached"
	}
	return true, ""
}
func (r *fakeRes) LowPriority() bool { return true }
func (r *fakeRes) Resumed(id, reason string, raise bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := id + ":" + reason
	if raise {
		s += ":raise"
	}
	r.resumed = append(r.resumed, s)
}

// fakeSuspend records pause/continue calls instead of signalling anything.
type fakeSuspend struct {
	mu    sync.Mutex
	calls []string
}

func (s *fakeSuspend) fn(pid int, stop bool) ([]sysinfo.Proc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op := "cont"
	if stop {
		op = "stop"
	}
	s.calls = append(s.calls, op)
	return []sysinfo.Proc{{PID: pid, Start: 1}}, nil
}

func (s *fakeSuspend) got() []string { s.mu.Lock(); defer s.mu.Unlock(); return slices.Clone(s.calls) }

func (e *env) fleetWith(res Resources, sus *fakeSuspend) *Fleet {
	e.t.Helper()
	f, err := New(Options{Elevated: func() bool { return false },
		Store: e.st, Paths: config.Paths{Home: e.home},
		Runners: map[model.Mode]runner.Runner{model.ModeChat: e.chat, model.ModeTerminal: e.term},
		Machine: "testbox", Version: "test", Workspace: e.workspace, URL: "http://127.0.0.1:1",
		Executable: "/opt/tmx/tmx", Resources: res, Suspend: sus.fn,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		f.Close(ctx)
	})
	if err := f.Start(); err != nil {
		e.t.Fatal(err)
	}
	return f
}

func launch(t *testing.T, f *Fleet, name, prompt string) model.Agent {
	t.Helper()
	a, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: name, Level: model.LevelWorker, Prompt: prompt})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestLaunchQueue(t *testing.T) {
	e := newEnv(t)
	res := &fakeRes{max: 2}
	f := e.fleetWith(res, &fakeSuspend{})
	w1 := launch(t, f, "w1", "task one")
	if w1.State == model.StateQueued {
		t.Fatal("w1 fits (head + w1 = 2)")
	}
	if p := e.chat.Last(w1.ID); !p.Spec.LowPriority {
		t.Fatal("agents start at low priority")
	}
	w2 := launch(t, f, "w2", "task two")
	w3 := launch(t, f, "w3", "")
	if w2.State != model.StateQueued || w2.QueuePos != 1 || w2.QueueReason != "limit reached" {
		t.Fatalf("w2: %s pos %d %q", w2.State, w2.QueuePos, w2.QueueReason)
	}
	if w3.State != model.StateQueued || w3.QueuePos != 2 {
		t.Fatalf("w3: %s pos %d", w3.State, w3.QueuePos)
	}
	if e.chat.Last(w2.ID) != nil {
		t.Fatal("a queued agent has no process")
	}
	// No-wait refuses and creates nothing.
	_, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "w4", NoWait: true})
	if KindOf(err) != KindTooMany || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("no-wait launch: %v", err)
	}
	if _, err := f.Agent("w4"); err == nil {
		t.Fatal("a refused launch must not create the agent")
	}
	if _, err := f.StartAgentWith(OwnerCaller, "w3", StartOptions{NoWait: true}); err != nil {
		t.Fatalf("starting an already queued agent keeps it queued: %v", err)
	}

	// Capacity frees: w2 starts with the prompt it was launched with; w3 moves up.
	if _, err := f.StopAgent(OwnerCaller, "w1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "w2 started", func() bool { return e.chat.Last(w2.ID) != nil })
	p2 := e.chat.Last(w2.ID)
	eventually(t, "w2 got its prompt", func() bool { return slices.Equal(p2.Sent(), []string{"task two"}) })
	eventually(t, "w3 first in line", func() bool { return mustAgent(t, f, "w3").QueuePos == 1 })

	// Start anyway: owner only.
	if _, err := f.StartAgentWith(AgentCaller(w2.ID), "w3", StartOptions{Force: true}); KindOf(err) != KindForbidden {
		t.Fatalf("agent force: %v", err)
	}
	a3, err := f.StartAgentWith(OwnerCaller, "w3", StartOptions{Force: true})
	if err != nil || a3.State == model.StateQueued || a3.QueuePos != 0 {
		t.Fatalf("start anyway: %+v %v", a3, err)
	}

	// Stopping a queued agent takes it out of the queue.
	w5 := launch(t, f, "w5", "")
	if w5.State != model.StateQueued {
		t.Fatalf("w5 %s", w5.State)
	}
	if a, _ := f.StopAgent(OwnerCaller, "w5"); a.State != model.StateStopped || a.QueuePos != 0 {
		t.Fatalf("stopped queued agent: %+v", a)
	}
	res.setMax(10)
	f.PumpQueue()
	if e.chat.Last(w5.ID) != nil {
		t.Fatal("a stopped agent must not start from the queue")
	}
}

func TestQueueSurvivesRestart(t *testing.T) {
	e := newEnv(t)
	res := &fakeRes{max: 2}
	f := e.fleetWith(res, &fakeSuspend{})
	launch(t, f, "w1", "")
	w2 := launch(t, f, "w2", "later")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	f.Close(ctx)
	cancel()

	f2 := e.fleetWith(res, &fakeSuspend{})
	// head and w1 (who ran) come back first; w2 waits again, prompt kept.
	if a := mustAgent(t, f2, "w2"); a.State != model.StateQueued || a.QueuePos != 1 {
		t.Fatalf("w2 after restart: %s %d", a.State, a.QueuePos)
	}
	if a := mustAgent(t, f2, "w1"); a.State == model.StateQueued {
		t.Fatal("w1 ran before: it must start first")
	}
	res.setMax(3)
	f2.PumpQueue()
	eventually(t, "w2 started with its prompt", func() bool {
		p := e.chat.Last(w2.ID)
		return p != nil && slices.Equal(p.Sent(), []string{"later"})
	})
}

func TestBrakeAndResume(t *testing.T) {
	e := newEnv(t)
	res := &fakeRes{max: 10}
	sus := &fakeSuspend{}
	f := e.fleetWith(res, sus)
	w := launch(t, f, "w1", "")
	p := e.chat.Last(w.ID)
	p.State(model.StateIdle)
	waitState(t, f, "w1", model.StateIdle)

	if _, err := f.BrakeAgent(w.ID, BrakeWriteBudget, "wrote 21 GB today; the budget is 20 GB", true); err != nil {
		t.Fatal(err)
	}
	a := mustAgent(t, f, "w1")
	if a.State != model.StatePaused || a.PausedReason != BrakeWriteBudget || a.PausedAt == 0 {
		t.Fatalf("paused agent: %+v", a)
	}
	if _, err := f.BrakeAgent(w.ID, BrakeDisk, "x", false); KindOf(err) != KindConflict {
		t.Fatalf("double pause: %v", err)
	}
	pend, _ := f.Approvals(model.ApprovalPending)
	if len(pend) != 1 || pend[0].Kind != model.ApprovalBrake || !slices.Contains(pend[0].Options, "Raise limit") {
		t.Fatalf("needs-you item: %+v", pend)
	}
	// While paused: runner state changes stay hidden, mail waits.
	p.State(model.StateWorking)
	if _, err := f.SendOwner("w1", "hello", ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if mustAgent(t, f, "w1").State != model.StatePaused || len(p.Sent()) != 0 {
		t.Fatalf("paused agent changed state or got mail: %s %v", mustAgent(t, f, "w1").State, p.Sent())
	}
	p.State(model.StateIdle)
	time.Sleep(20 * time.Millisecond)

	// Raise the limit: continued, state restored, mail delivered, resource control told.
	if _, err := f.Decide(OwnerCaller, pend[0].ID, DecideRequest{Decision: "raise"}); err != nil {
		t.Fatal(err)
	}
	if got := sus.got(); !slices.Equal(got, []string{"stop", "cont"}) {
		t.Fatalf("suspend calls %v", got)
	}
	eventually(t, "mail delivered after resume", func() bool { return len(p.Sent()) == 1 })
	if a := mustAgent(t, f, "w1"); a.PausedReason != "" {
		t.Fatalf("after resume: %+v", a)
	}
	if !slices.Equal(res.resumed, []string{w.ID + ":" + BrakeWriteBudget + ":raise"}) {
		t.Fatalf("resumed calls %v", res.resumed)
	}

	// A disk turn ends on its own; an unrelated reason does not resume it.
	if _, err := f.BrakeAgent(w.ID, BrakeDisk, "the disk is saturated", false); err != nil {
		t.Fatal(err)
	}
	if f.UnbrakeAgent(w.ID, BrakeWriteBudget, "") {
		t.Fatal("unbrake with another reason must not resume")
	}
	if !f.UnbrakeAgent(w.ID, BrakeDisk, "the disk recovered") {
		t.Fatal("unbrake did not resume")
	}
	if _, err := f.ResumeAgent(OwnerCaller, "w1"); KindOf(err) != KindConflict {
		t.Fatalf("resume of a running agent: %v", err)
	}

	// Stopping a paused agent continues it first, so it can end gracefully.
	if _, err := f.BrakeAgent(w.ID, BrakeMemory, "uses 9 GB", true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.StopAgent(OwnerCaller, "w1"); err != nil {
		t.Fatal(err)
	}
	if got := sus.got(); got[len(got)-1] != "cont" {
		t.Fatalf("stop did not continue first: %v", got)
	}
	if pend, _ := f.Approvals(model.ApprovalPending); len(pend) != 0 {
		t.Fatalf("the brake item must expire with the stop: %+v", pend)
	}
	if a := mustAgent(t, f, "w1"); a.State != model.StateStopped || a.PausedReason != "" {
		t.Fatalf("stopped: %+v", a)
	}
}

func TestOwnerResumeAndDeny(t *testing.T) {
	e := newEnv(t)
	res := &fakeRes{max: 10}
	f := e.fleetWith(res, &fakeSuspend{})
	w := launch(t, f, "w1", "")
	e.chat.Last(w.ID).State(model.StateIdle)
	waitState(t, f, "w1", model.StateIdle)
	if _, err := f.BrakeAgent(w.ID, BrakeWriteBudget, "over budget", true); err != nil {
		t.Fatal(err)
	}
	pend, _ := f.Approvals(model.ApprovalPending)
	if _, err := f.Decide(OwnerCaller, pend[0].ID, DecideRequest{Decision: "deny"}); err != nil {
		t.Fatal(err)
	}
	if a := mustAgent(t, f, "w1"); a.State != model.StatePaused {
		t.Fatal("deny keeps the agent paused")
	}
	// A worker may not resume others; the owner may.
	if _, err := f.ResumeAgent(AgentCaller(w.ID), "w1"); KindOf(err) != KindForbidden {
		t.Fatalf("self resume: %v", err)
	}
	if a, err := f.ResumeAgent(OwnerCaller, "w1"); err != nil || a.State != model.StateIdle {
		t.Fatalf("owner resume: %+v %v", a, err)
	}
	if !slices.Equal(res.resumed, []string{w.ID + ":" + BrakeWriteBudget}) {
		t.Fatalf("resumed %v", res.resumed)
	}
}

func TestBrakesNeverTouchShellOrProtected(t *testing.T) {
	e := newEnv(t)
	sus := &fakeSuspend{}
	f := e.fleetWith(&fakeRes{max: 10}, sus)
	sh, err := f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "owner-shell", Harness: model.HarnessShell})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.BrakeAgent(sh.ID, BrakeMemory, "test", false); err == nil {
		t.Fatal("owner shell was braked")
	}
	if calls := sus.got(); len(calls) != 0 {
		t.Fatalf("shell signalled: %v", calls)
	}
}
