package fleet

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/runner/fake"
)

// hostedRunner is a terminal runner whose processes live in session hosts: Detach leaves the
// fake process running, Reattach hands it back with the credential it was started with.
type hostedRunner struct {
	*fake.Runner
	mu         sync.Mutex
	live       map[string]*fake.Process // agent id → its running program
	cred       map[string]string
	reattached int
	unsure     int // the next unsure Reattach answers (ErrHostUnknown)
}

func newHostedRunner(r *fake.Runner) *hostedRunner {
	return &hostedRunner{Runner: r, live: map[string]*fake.Process{}, cred: map[string]string{}}
}

func (h *hostedRunner) Start(ctx context.Context, spec runner.Spec) (runner.Process, error) {
	p, err := h.Runner.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	fp := p.(*fake.Process)
	h.mu.Lock()
	h.live[spec.AgentID], h.cred[spec.AgentID] = fp, spec.HostToken
	h.mu.Unlock()
	return newHostedProc(fp), nil
}

func (h *hostedRunner) Reattach(_ context.Context, spec runner.Spec) (runner.Process, string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.unsure > 0 {
		h.unsure--
		return nil, "", runner.ErrHostUnknown
	}
	fp := h.live[spec.AgentID]
	if fp == nil || fp.Stopped() {
		return nil, "", runner.ErrNoHost
	}
	h.reattached++
	return newHostedProc(fp), h.cred[spec.AgentID], nil
}

// hostedProc is one daemon's view of a hosted fake process.
type hostedProc struct {
	*fake.Process
	ev   chan runner.Event
	stop chan struct{}
	once sync.Once
}

func newHostedProc(p *fake.Process) *hostedProc {
	h := &hostedProc{Process: p, ev: make(chan runner.Event, 1024), stop: make(chan struct{})}
	go func() {
		defer close(h.ev)
		for {
			select {
			case <-h.stop:
				return
			case e, ok := <-p.Events():
				if !ok {
					return
				}
				h.ev <- e
			}
		}
	}()
	return h
}

func (h *hostedProc) Events() <-chan runner.Event { return h.ev }
func (h *hostedProc) Detach() bool                { h.once.Do(func() { close(h.stop) }); return true }

// A daemon restart leaves a terminal agent running in its session host: Close detaches (no
// stop, no exit in the timeline), and the next daemon reattaches to the same program, with
// the credential it was started with, instead of starting it again (the owner, 4 Nov: restarts
// cut the agents).
func TestDaemonRestartKeepsHostedAgentRunning(t *testing.T) {
	e := newEnv(t)
	hr := newHostedRunner(e.term)
	e.hostExe, e.termRunner = "/opt/tmx/tmx-host", hr
	f1 := e.started()
	a, err := f1.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f1), Name: "w-hosted", Mode: model.ModeTerminal})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "started", func() bool { return e.term.Last(a.ID) != nil })
	prog := e.term.Last(a.ID)
	if prog.Spec.HostDir == "" || prog.Spec.HostExe != "/opt/tmx/tmx-host" || prog.Spec.HostToken == "" {
		t.Fatalf("not hosted: %+v", prog.Spec)
	}
	cred := prog.Spec.HostToken
	starts := len(e.term.Procs())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f1.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if prog.Stopped() {
		t.Fatal("the daemon's stop stopped the hosted agent")
	}
	tl, _ := e.st.Timeline(a.ID, 0, 1000)
	for _, en := range tl {
		if strings.Contains(en.Text, "process exited") || en.Text == "stopped" {
			t.Fatalf("a detach was logged as an exit: %+v", en)
		}
	}

	f2 := e.started()
	eventually(t, "reattached", func() bool {
		got, err := f2.Agent(a.ID)
		return err == nil && got.PID == prog.PID()
	})
	if len(e.term.Procs()) != starts {
		t.Fatalf("the agent was started again (%d starts, was %d)", len(e.term.Procs()), starts)
	}
	hr.mu.Lock()
	n := hr.reattached
	hr.mu.Unlock()
	if n != 1 {
		t.Fatalf("reattached %d times", n)
	}
	if c, ok := f2.CallerForToken(cred); !ok || c.AgentID != a.ID {
		t.Fatalf("the program's credential is not known to the new daemon: %+v %v", c, ok)
	}
	// The new daemon talks to the same program (mail waits for it to be idle, as always).
	prog.State(model.StateIdle)
	if _, err := f2.SendOwner(a.ID, "still there?", ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, "delivered", func() bool {
		for _, s := range prog.Sent() {
			if strings.Contains(s, "still there?") {
				return true
			}
		}
		return false
	})
}

// No host to reattach to (it ended, or a stale host.json was refused): the agent is started
// again, resuming its conversation, as before.
func TestDaemonRestartStartsAgentWithoutHost(t *testing.T) {
	e := newEnv(t)
	hr := newHostedRunner(e.term)
	e.hostExe, e.termRunner = "/opt/tmx/tmx-host", hr
	f1 := e.started()
	a, err := f1.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f1), Name: "w-gone", Mode: model.ModeTerminal})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "started", func() bool { return e.term.Last(a.ID) != nil })
	prog := e.term.Last(a.ID)
	prog.Emit(runner.Event{Kind: runner.EvSession, SessionID: "conv-1"})
	eventually(t, "session known", func() bool {
		got, err := f1.Agent(a.ID)
		return err == nil && got.SessionID == "conv-1"
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = f1.Close(ctx)
	_ = prog.Stop(ctx) // the host ended meanwhile
	starts := len(e.term.Procs())
	e.started()
	eventually(t, "started again", func() bool { return len(e.term.Procs()) == starts+1 })
	// The same conversation: resumed, or (not confirmed in this test's config folder) passed as
	// its id.
	again := e.term.Last(a.ID)
	if again.Spec.SessionID != "conv-1" && !slices.Contains(again.Spec.ExtraArgs, "conv-1") {
		t.Fatalf("not the same conversation: %q %q", again.Spec.SessionID, again.Spec.ExtraArgs)
	}
}

// Chat mode (Hostb's agents: claude stream-json): the same. A codex chat agent is not hosted.
func TestDaemonRestartKeepsHostedChatAgentRunning(t *testing.T) {
	e := newEnv(t)
	hr := newHostedRunner(e.chat)
	e.hostExe, e.chatRunner = "/opt/tmx/tmx-host", hr
	f1 := e.started()
	a, err := f1.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f1), Name: "w-chat"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "started", func() bool { return e.chat.Last(a.ID) != nil })
	prog := e.chat.Last(a.ID)
	if prog.Spec.HostDir == "" || prog.Spec.HostToken == "" {
		t.Fatalf("not hosted: %+v", prog.Spec)
	}
	cred := prog.Spec.HostToken
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f1.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if prog.Stopped() {
		t.Fatal("the daemon's stop stopped the hosted chat agent")
	}
	starts := len(e.chat.Procs())
	f2 := e.started()
	eventually(t, "reattached", func() bool {
		got, err := f2.Agent(a.ID)
		return err == nil && got.PID == prog.PID()
	})
	if len(e.chat.Procs()) != starts {
		t.Fatalf("the head or the agent was started again (%d starts, was %d)", len(e.chat.Procs()), starts)
	}
	if c, ok := f2.CallerForToken(cred); !ok || c.AgentID != a.ID {
		t.Fatalf("credential not known: %+v %v", c, ok)
	}
}

func TestHostedHarnesses(t *testing.T) {
	f := &Fleet{opts: Options{HostExecutable: "/x"}}
	for _, tc := range []struct {
		a    model.Agent
		want bool
	}{
		{model.Agent{Mode: model.ModeChat, Harness: model.HarnessClaude}, true},
		{model.Agent{Mode: model.ModeChat}, true},
		{model.Agent{Mode: model.ModeChat, Harness: model.HarnessCodex}, false},
		{model.Agent{Mode: model.ModeTerminal, Harness: model.HarnessCodex}, true},
		{model.Agent{Mode: model.ModeTerminal, Harness: model.HarnessShell}, true},
	} {
		if got := f.hostedLocked(tc.a); got != tc.want {
			t.Errorf("%+v: %v", tc.a, got)
		}
	}
	if (&Fleet{}).hostedLocked(model.Agent{Mode: model.ModeTerminal}) {
		t.Error("hosted without an executable")
	}
}

// The new daemon cannot tell whether the host still runs (the process list unreadable): it does
// not start a second copy on the same conversation, says so, checks again, and reattaches once
// it can (tmx-lead, o5: #247).
func TestUnsureHostIsNotStartedTwice(t *testing.T) {
	old := hostRetry
	hostRetry = 50 * time.Millisecond
	defer func() { hostRetry = old }()
	e := newEnv(t)
	hr := newHostedRunner(e.term)
	e.hostExe, e.termRunner = "/opt/tmx/tmx-host", hr
	f1 := e.started()
	a, err := f1.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f1), Name: "w-unsure", Mode: model.ModeTerminal})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "started", func() bool { return e.term.Last(a.ID) != nil })
	prog := e.term.Last(a.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = f1.Close(ctx)
	starts := len(e.term.Procs())
	hr.mu.Lock()
	hr.unsure = 2
	hr.mu.Unlock()

	f2 := e.started()
	eventually(t, "reattached after the checks", func() bool {
		got, err := f2.Agent(a.ID)
		return err == nil && got.PID == prog.PID()
	})
	if len(e.term.Procs()) != starts {
		t.Fatalf("a second copy was started (%d starts, was %d)", len(e.term.Procs()), starts)
	}
	tl, _ := e.st.Timeline(a.ID, 0, 1000)
	reattached := false
	for _, en := range tl {
		reattached = reattached || strings.Contains(en.Text, "reattached")
	}
	if !reattached {
		t.Fatal("no reattach in the timeline")
	}
}

// An agent whose host stays unknown is still stoppable by the owner: the stop ends it by the
// identity its host recorded (host.Stop), never by name; a host that cannot be verified is not
// killed, and that is said (tmx-lead: never an agent the owner cannot stop for 12 h).
func TestUnknownHostAgentCanBeStopped(t *testing.T) {
	old, oldStop := hostRetry, stopHost
	hostRetry = time.Hour // stays unknown during the test
	defer func() { hostRetry, stopHost = old, oldStop }()
	var stopped []string
	stopErr := error(nil)
	stopHost = func(dir string) (bool, error) {
		stopped = append(stopped, dir)
		return false, stopErr
	}
	e := newEnv(t)
	hr := newHostedRunner(e.term)
	e.hostExe, e.termRunner = "/opt/tmx/tmx-host", hr
	f1 := e.started()
	a, err := f1.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f1), Name: "w-stuck", Mode: model.ModeTerminal})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "started", func() bool { return e.term.Last(a.ID) != nil })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = f1.Close(ctx)
	hr.mu.Lock()
	hr.unsure = 1000
	hr.mu.Unlock()
	f2 := e.started()
	eventually(t, "unknown", func() bool {
		got, err := f2.Agent(a.ID)
		return err == nil && strings.Contains(got.Error, "could not be checked")
	})

	// The host cannot be verified: not killed, and the owner is told.
	stopErr = runner.ErrHostUnknown
	if _, err := f2.StopAgent(OwnerCaller, a.ID); err != nil {
		t.Fatal(err)
	}
	if len(stopped) != 1 || stopped[0] != f2.hostDir(a.ID) {
		t.Fatalf("stop went to %v", stopped)
	}
	got, _ := f2.Agent(a.ID)
	if got.State != model.StateError || !strings.Contains(got.Error, "could not stop it") {
		t.Fatalf("after an unverifiable stop: %s %q", got.State, got.Error)
	}

	// Verified: the host is ended, the agent is stopped.
	stopErr = nil
	if _, err := f2.StopAgent(OwnerCaller, a.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = f2.Agent(a.ID)
	if len(stopped) != 2 || got.State != model.StateStopped || got.Desired != "stopped" {
		t.Fatalf("after the stop: %v %s %s", stopped, got.State, got.Desired)
	}
	tl, _ := e.st.Timeline(a.ID, 0, 1000)
	ended := false
	for _, en := range tl {
		ended = ended || en.Text == "stopped: its session host was ended"
	}
	if !ended {
		t.Fatal("the stop is not in the timeline")
	}
}

// While the owner's stop ends an unknown host, nothing starts the agent (a second copy), and
// the stop's outcome is not written over a process that runs. o5, #249.
func TestNoStartWhileAnUnknownHostIsStopped(t *testing.T) {
	old, oldStop := hostRetry, stopHost
	hostRetry = time.Hour
	defer func() { hostRetry, stopHost = old, oldStop }()
	release := make(chan struct{})
	inStop := make(chan struct{}, 1)
	stopHost = func(string) (bool, error) {
		inStop <- struct{}{}
		<-release
		return false, nil
	}
	e := newEnv(t)
	hr := newHostedRunner(e.term)
	e.hostExe, e.termRunner = "/opt/tmx/tmx-host", hr
	f1 := e.started()
	a, err := f1.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f1), Name: "w-race", Mode: model.ModeTerminal})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "started", func() bool { return e.term.Last(a.ID) != nil })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = f1.Close(ctx)
	hr.mu.Lock()
	hr.unsure = 1000
	hr.mu.Unlock()
	f2 := e.started()
	eventually(t, "unknown", func() bool {
		got, err := f2.Agent(a.ID)
		return err == nil && strings.Contains(got.Error, "could not be checked")
	})
	starts := len(e.term.Procs())
	stopDone := make(chan struct{})
	go func() { _, _ = f2.StopAgent(OwnerCaller, a.ID); close(stopDone) }()
	<-inStop
	if _, err := f2.StartAgent(OwnerCaller, a.ID); err == nil {
		t.Fatal("a start was accepted while the host was being stopped")
	}
	close(release)
	<-stopDone
	if len(e.term.Procs()) != starts {
		t.Fatalf("a second copy was started (%d starts, was %d)", len(e.term.Procs()), starts)
	}
	if got, _ := f2.Agent(a.ID); got.State != model.StateStopped {
		t.Fatalf("after the stop: %s", got.State)
	}
}
