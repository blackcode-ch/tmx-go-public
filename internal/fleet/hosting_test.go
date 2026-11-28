package fleet

import (
	"context"
	"errors"
	"fmt"
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
	mu           sync.Mutex
	live         map[string]*fake.Process // agent id → its running program
	cred         map[string]string
	reattached   int
	silent       bool
	repaintDelay time.Duration
	repaint      int // verified host without a fresh screen
	unsure       int // the next unsure Reattach answers (ErrHostUnknown)
	// sibling: the runner of the agent's other mode. An agent has ONE session host: when this
	// runner has no live program for it but the sibling has, the host runs the other mode.
	sibling *hostedRunner
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

func (h *hostedRunner) Reattach(ctx context.Context, spec runner.Spec) (runner.Process, string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.repaint > 0 {
		h.repaint--
		if h.repaintDelay > 0 {
			delay := h.repaintDelay
			h.repaintDelay = 0 // retry assertions need not repeat the real wait
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, "", errors.Join(runner.ErrHostUnknown, ctx.Err())
			}
		}
		err := errors.Join(runner.ErrHostUnknown, runner.ErrHostRepaint)
		if h.silent {
			err = errors.Join(err, runner.ErrHostSilent)
		}
		return nil, "", err
	}
	if h.unsure > 0 {
		h.unsure--
		return nil, "", runner.ErrHostUnknown
	}
	fp := h.live[spec.AgentID]
	if fp == nil || fp.Stopped() {
		if h.sibling != nil && h.sibling.runs(spec.AgentID) {
			return nil, "", runner.ErrHostMode
		}
		return nil, "", runner.ErrNoHost
	}
	h.reattached++
	return newHostedProc(fp), h.cred[spec.AgentID], nil
}

// runs reports whether this runner's program for the agent is still running in its host.
func (h *hostedRunner) runs(agentID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	fp := h.live[agentID]
	return fp != nil && !fp.Stopped()
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

func TestReattachedWorkingChatKeepsOwnerMailQueuedUntilResult(t *testing.T) {
	e := newEnv(t)
	e.hostExe, e.chatRunner = "/opt/tmx/tmx-host", newHostedRunner(e.chat)
	f1 := e.started()
	a, prog := talkingAgent(t, e, f1, "hosted-owner-queue")
	prog.State(model.StateWorking)
	waitState(t, f1, a.ID, model.StateWorking)
	before := len(prog.Sent())
	var queued []model.Message
	for _, text := range []string{"first waiting owner turn", "second waiting owner turn"} {
		m, err := f1.SendOwner(a.ID, text, "")
		if err != nil || m.State != model.MsgQueued {
			t.Fatalf("send while busy: %+v %v", m, err)
		}
		queued = append(queued, m)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f1.Close(ctx); err != nil {
		t.Fatal(err)
	}
	f2 := e.started()
	eventually(t, "same process attached before state is available", func() bool {
		a, err := f2.Agent(a.ID)
		return err == nil && a.PID == prog.PID()
	})
	f2.tryDeliver(a.ID)
	assertQueued := func() {
		t.Helper()
		if len(prog.Sent()) != before || prog.Interrupts() != 0 {
			t.Fatalf("handoff cut into live hosted turn: %q", prog.Sent()[before:])
		}
		for _, m := range queued {
			got, err := e.st.GetMessage(m.ID)
			if err != nil || got.State != model.MsgQueued {
				t.Fatalf("claimed before idle: %+v %v", got, err)
			}
		}
	}
	assertQueued()
	prog.State(model.StateWorking)
	waitState(t, f2, a.ID, model.StateWorking)
	assertQueued()
	prog.Turn("answer from the original turn", 0, 1)
	for i, m := range queued {
		eventually(t, m.Body, func() bool { return len(prog.Sent()) >= before+i+1 })
		if sent := prog.Sent()[before:]; len(sent) != i+1 || !strings.Contains(sent[i], m.Body) {
			t.Fatalf("handoff %d: %q", i, sent)
		}
		prog.Turn("answer from queued turn", 0, 1)
	}
	waitState(t, f2, a.ID, model.StateIdle)
	if got := mustAgent(t, f2, a.ID); got.PID != prog.PID() || got.SessionID != a.SessionID ||
		len(prog.Sent()) != before+len(queued) || prog.Interrupts() != 0 {
		t.Fatalf("lost continuity across attachment: %+v", got)
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

func TestLegacyRepaintRecoveryResumesOnce(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		before                     model.AgentState
		silent, stopFails, restart bool
	}{
		{"silent idle", model.StateIdle, true, false, true},
		{"real budget silent idle", model.StateIdle, true, false, true},
		{"real budget unrecognized", model.StateIdle, false, false, false},
		{"silent starting", model.StateStarting, true, false, true},
		{"stop failure", model.StateIdle, true, true, true},
		{"working before handover", model.StateWorking, true, false, false},
		{"approval before handover", model.StateWaiting, true, false, false},
		{"idle but output", model.StateIdle, false, false, false},
		{"unknown before handover", model.StateError, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldStop, oldRetry := stopHost, hostRetry
			defer func() { stopHost, hostRetry = oldStop, oldRetry }()
			hostRetry = time.Hour
			e := newEnv(t)
			hr := newHostedRunner(e.term)
			e.hostExe, e.termRunner = "/opt/tmx/tmx-host", hr
			f1 := e.started()
			a, err := f1.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f1), Name: "legacy", Mode: model.ModeTerminal})
			if err != nil {
				t.Fatal(err)
			}
			eventually(t, "started", func() bool { return e.term.Last(a.ID) != nil })
			prog := e.term.Last(a.ID)
			prog.Emit(runner.Event{Kind: runner.EvSession, SessionID: "legacy-conversation"})
			prog.Emit(runner.Event{Kind: runner.EvTimeline, Entry: &model.TimelineEntry{Kind: model.EntryAssistant, Text: "existing answer"}})
			prog.Emit(runner.Event{Kind: runner.EvState, State: tc.before})
			eventually(t, "saved pre-handover state", func() bool {
				got, _ := f1.Agent(a.ID)
				return got.SessionID == "legacy-conversation" && got.State == tc.before
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := f1.Close(ctx); err != nil {
				t.Fatal(err)
			}
			calls := 0
			stopHost = func(string) (bool, error) {
				calls++
				if tc.stopFails {
					return false, runner.ErrHostUnknown
				}
				return false, prog.Stop(context.Background())
			}
			hr.mu.Lock()
			hr.repaint = 100
			hr.silent = tc.silent
			if strings.HasPrefix(tc.name, "real budget") {
				// Real repaint timeout plus the mandatory 100ms resize jiggle.
				hr.repaintDelay = 20*time.Second + 100*time.Millisecond
			}
			hr.mu.Unlock()
			f2 := e.started()
			if tc.restart && !tc.stopFails {
				eventually(t, "resumed", func() bool { return e.term.Last(a.ID) != prog })
				if !prog.Stopped() {
					t.Fatal("old agent not stopped")
				}
				if next := e.term.Last(a.ID); next.Spec.SessionID != "legacy-conversation" {
					t.Fatalf("lost resume: %+v", next.Spec)
				}
			} else {
				eventually(t, "needs attention", func() bool { got, _ := f2.Agent(a.ID); return strings.Contains(got.Error, "needs attention") })
				hr.mu.Lock()
				hr.silent = true // later silence must not override an earlier activity warning
				hr.mu.Unlock()
				if err := f2.startAgentOpts(a.ID, "", startOpts{reattach: true}); err == nil {
					t.Fatal("expected unknown host")
				}
				if e.term.Last(a.ID) != prog || prog.Stopped() {
					t.Fatal("uncertain live agent was stopped or duplicated")
				}
			}
			wantCalls := 0
			if tc.restart {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("stops=%d want %d", calls, wantCalls)
			}
			tl, _ := e.st.Timeline(a.ID, 0, 1000)
			notes := 0
			for _, en := range tl {
				if tc.restart && strings.Contains(en.Text, "restarting the agent once") {
					notes++
				}
				if !tc.restart && en.Meta["terminal_recovery"] == "needs_attention" {
					notes++
				}
			}
			if notes != 1 {
				t.Fatalf("visible notes=%d", notes)
			}
		})
	}
}

func TestOwnerRestartNeedsAttentionHostResumesConversation(t *testing.T) {
	for _, stopFails := range []bool{false, true} {
		t.Run(fmt.Sprint(stopFails), func(t *testing.T) {
			oldStop, oldRetry := stopHost, hostRetry
			defer func() { stopHost, hostRetry = oldStop, oldRetry }()
			hostRetry = time.Hour
			e := newEnv(t)
			hr := newHostedRunner(e.term)
			e.hostExe, e.termRunner = "/opt/tmx/tmx-host", hr
			f1 := e.started()
			a, err := f1.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f1), Name: "needs-restart", Mode: model.ModeTerminal})
			if err != nil {
				t.Fatal(err)
			}
			eventually(t, "started", func() bool { return e.term.Last(a.ID) != nil })
			prog := e.term.Last(a.ID)
			prog.Emit(runner.Event{Kind: runner.EvSession, SessionID: "keep-conversation"})
			prog.Emit(runner.Event{Kind: runner.EvTimeline, Entry: &model.TimelineEntry{Kind: model.EntryAssistant, Text: "existing answer"}})
			prog.Emit(runner.Event{Kind: runner.EvState, State: model.StateWorking})
			eventually(t, "working", func() bool {
				got, _ := f1.Agent(a.ID)
				return got.SessionID == "keep-conversation" && got.State == model.StateWorking
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := f1.Close(ctx); err != nil {
				t.Fatal(err)
			}
			calls := 0
			stopHost = func(dir string) (bool, error) {
				calls++
				if dir != prog.Spec.HostDir {
					t.Errorf("wrong host: %s", dir)
				}
				if stopFails {
					return false, runner.ErrHostUnknown
				}
				return false, prog.Stop(context.Background())
			}
			hr.mu.Lock()
			hr.repaint = 100
			hr.mu.Unlock()
			f2 := e.started()
			got, _ := f2.Agent(a.ID)
			if !strings.Contains(got.Error, "needs attention") || calls != 0 {
				t.Fatalf("before owner restart: %+v stops=%d", got, calls)
			}
			// Shutdown's keepDesired path must continue preserving the hosted program.
			f2.stopProcess(ctx, a.ID, true)
			if calls != 0 || prog.Stopped() {
				t.Fatal("shutdown path stopped unknown host")
			}
			_, err = f2.RestartAgent(OwnerCaller, a.ID)
			if calls != 1 {
				t.Fatalf("owner restart stop calls=%d", calls)
			}
			if stopFails {
				if err == nil || prog.Stopped() || e.term.Last(a.ID) != prog {
					t.Fatalf("failed stop launched duplicate: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				next := e.term.Last(a.ID)
				if next == prog || !prog.Stopped() || next.Spec.SessionID != "keep-conversation" {
					t.Fatalf("did not stop then resume: %+v", next.Spec)
				}
			}
		})
	}
}
