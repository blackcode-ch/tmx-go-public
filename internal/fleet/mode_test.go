package fleet

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/harness"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/runner/fake"
)

// quickSwitch makes switches run as soon as the agent is idle (no settle time).
func quickSwitch(t *testing.T) {
	t.Helper()
	old := switchSettle
	switchSettle = 0
	t.Cleanup(func() { switchSettle = old })
}

// talkingAgent launches a Claude chat agent whose conversation exists (claude confirmed its
// session) and that is idle after one turn.
func talkingAgent(t *testing.T, e *env, f *Fleet, name string) (model.Agent, *fake.Process) {
	t.Helper()
	a, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: name, Prompt: "remember the word kumquat"})
	if err != nil {
		t.Fatal(err)
	}
	p := e.chat.Last(a.ID)
	p.Emit(runner.Event{Kind: runner.EvSession, SessionID: a.SessionID})
	p.Turn("I will remember kumquat.", 0.01, 1)
	waitState(t, f, name, model.StateIdle)
	return mustAgent(t, f, name), p
}

func newProc(t *testing.T, r *fake.Runner, id string, after *fake.Process) *fake.Process {
	t.Helper()
	var p *fake.Process
	eventually(t, "a new process", func() bool { p = r.Last(id); return p != nil && p != after })
	return p
}

func hasEntry(t *testing.T, f *Fleet, name string, kind model.EntryKind, text string) bool {
	t.Helper()
	tl, err := f.Timeline(name, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range tl {
		if e.Kind == kind && strings.Contains(e.Text, text) {
			return true
		}
	}
	return false
}

func TestSwitchToTheScreenAndBackKeepsTheSession(t *testing.T) {
	quickSwitch(t)
	e := newEnv(t)
	f := e.started()
	a, chat := talkingAgent(t, e, f, "w")
	sid := a.SessionID
	chatSpec := chat.Spec

	res, err := f.SetMode(OwnerCaller, "w", ModeRequest{Mode: model.ModeTerminal})
	if err != nil || res.Pending {
		t.Fatalf("switch: %+v %v", res, err)
	}
	tui := newProc(t, e.term, a.ID, nil)
	waitState(t, f, "w", model.StateStarting)
	select {
	case <-chat.Done():
	default:
		t.Fatal("the chat process still runs")
	}
	s := tui.Spec
	if s.Mode != model.ModeTerminal || s.SessionID != sid || slices.Contains(s.ExtraArgs, "--session-id") {
		t.Fatalf("the screen does not resume the conversation: %+v", s)
	}
	// Same profile, tools, MCP, instructions and guard as in chat mode.
	if s.Model != chatSpec.Model || s.PermissionMode != chatSpec.PermissionMode || s.SystemPrompt != chatSpec.SystemPrompt ||
		!slices.Equal(s.AllowedTools, chatSpec.AllowedTools) || !slices.Equal(s.DisallowedTools, chatSpec.DisallowedTools) ||
		s.MCPConfigPath == "" || s.SettingsPath == "" || !slices.Equal(s.AddDirs, chatSpec.AddDirs) {
		t.Fatalf("the screen lost settings:\nchat %+v\ntui  %+v", chatSpec, s)
	}
	s.Command = "/bin/true" // the args claude's TUI gets (no claude needed)
	cmd, err := harness.TerminalCommand(s)
	if err != nil || !hasArgs(cmd.Args, "--resume", sid) || !hasArgs(cmd.Args, "--settings", s.SettingsPath) {
		t.Fatalf("TUI args %v %v", cmd.Args, err)
	}
	got := mustAgent(t, f, "w")
	if got.Mode != model.ModeTerminal || got.HomeMode != model.ModeChat || got.PendingMode != "" || got.SessionID != sid {
		t.Fatalf("after the switch: %+v", got)
	}
	if stored, _ := e.st.GetAgent(a.ID); stored.Mode != model.ModeChat {
		t.Fatalf("the store must keep the launch mode, has %s", stored.Mode)
	}
	if !hasEntry(t, f, "w", model.EntrySystem, "Switched to Claude Code's own screen") {
		t.Fatal("no timeline note for the switch")
	}

	tui.State(model.StateIdle)
	waitState(t, f, "w", model.StateIdle)
	if res, err := f.SetMode(OwnerCaller, "w", ModeRequest{Mode: model.ModeChat}); err != nil || res.Pending {
		t.Fatalf("back: %+v %v", res, err)
	}
	back := newProc(t, e.chat, a.ID, chat)
	if back.Spec.Mode != model.ModeChat || back.Spec.SessionID != sid {
		t.Fatalf("chat does not resume the conversation: %+v", back.Spec)
	}
	eventually(t, "home", func() bool { g := mustAgent(t, f, "w"); return g.Mode == model.ModeChat && g.HomeMode == "" })
	if !hasEntry(t, f, "w", model.EntrySystem, "Back in chat") {
		t.Fatal("no timeline note for the way back")
	}
}

func hasArgs(args []string, flag, val string) bool {
	for i := range args {
		if args[i] == flag && i+1 < len(args) && args[i+1] == val {
			return true
		}
	}
	return false
}

func TestSwitchWaitsForTheTurnAndHoldsMail(t *testing.T) {
	quickSwitch(t)
	e := newEnv(t)
	f := e.started()
	a, chat := talkingAgent(t, e, f, "w")
	chat.State(model.StateWorking)
	waitState(t, f, "w", model.StateWorking)

	res, err := f.SetMode(OwnerCaller, "w", ModeRequest{Mode: model.ModeTerminal})
	if err != nil || !res.Pending || res.Agent.PendingMode != model.ModeTerminal {
		t.Fatalf("mid-turn: %+v %v", res, err)
	}
	// The owner writes while the switch waits: the message is not pushed into the old process.
	n := len(chat.Sent())
	if _, err := f.SendOwner("w", "what was the word?", ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if len(chat.Sent()) != n || e.term.Last(a.ID) != nil {
		t.Fatal("the turn was cut short or the mail went to the old process")
	}
	// Cancel and ask again: nothing changes until the turn ends.
	if res, _ := f.SetMode(OwnerCaller, "w", ModeRequest{Mode: model.ModeChat}); res.Agent.PendingMode != "" {
		t.Fatal("not cancelled")
	}
	if res, _ := f.SetMode(OwnerCaller, "w", ModeRequest{Mode: model.ModeTerminal}); !res.Pending {
		t.Fatal("not pending again")
	}

	chat.Turn("done", 0, 1)
	tui := newProc(t, e.term, a.ID, nil)
	if mustAgent(t, f, "w").Mode != model.ModeTerminal {
		t.Fatal("not switched at the end of the turn")
	}
	if len(tui.Sent()) != 0 {
		t.Fatal("mail typed before the screen was ready")
	}
	tui.State(model.StateIdle)
	eventually(t, "the held message is typed into the screen", func() bool {
		s := tui.Sent()
		return len(s) == 1 && strings.Contains(s[0], "what was the word?")
	})
}

func TestSwitchWaitsForApprovalsInBothModes(t *testing.T) {
	quickSwitch(t)
	e := newEnv(t)
	f := e.started()
	a, chat := talkingAgent(t, e, f, "w")

	// Chat mode: a permission request is part of the turn.
	chat.State(model.StateWorking)
	chat.Emit(runner.Event{Kind: runner.EvApproval, Approval: &runner.ApprovalRequest{ID: "req-1", Tool: "Bash"}})
	waitState(t, f, "w", model.StateWaiting)
	if res, _ := f.SetMode(OwnerCaller, "w", ModeRequest{Mode: model.ModeTerminal}); !res.Pending {
		t.Fatal("switched with an open approval")
	}
	ap := pendingFor(t, f, a.ID)
	if _, err := f.Decide(OwnerCaller, ap.ID, DecideRequest{Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	if len(chat.Answers()) != 1 || e.term.Last(a.ID) != nil {
		t.Fatal("the answer did not reach the chat process, or it switched before the turn ended")
	}
	chat.Turn("ran it", 0, 1)
	tui := newProc(t, e.term, a.ID, nil)
	tui.State(model.StateIdle)
	waitState(t, f, "w", model.StateIdle)

	// Its own screen: a menu becomes an approval, answered through the screen's runner.
	tui.State(model.StateWorking)
	tui.Emit(runner.Event{Kind: runner.EvApproval, Approval: &runner.ApprovalRequest{ID: "menu-1", Tool: model.ToolTerminalMenu,
		Question: "Do you want to proceed?", Options: []string{"Yes", "No"}}})
	waitState(t, f, "w", model.StateWaiting)
	if res, _ := f.SetMode(OwnerCaller, "w", ModeRequest{Mode: model.ModeChat}); !res.Pending {
		t.Fatal("switched back with a menu open")
	}
	ap = pendingFor(t, f, a.ID)
	if ap.Tool != model.ToolTerminalMenu {
		t.Fatalf("approval %+v", ap)
	}
	if _, err := f.Decide(OwnerCaller, ap.ID, DecideRequest{Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
	if ans := tui.Answers(); len(ans) != 1 || ans[0].RequestID != "menu-1" || !ans[0].Decision.Allow {
		t.Fatalf("answers %+v", ans)
	}
	tui.State(model.StateIdle)
	back := newProc(t, e.chat, a.ID, chat)
	if back.Spec.SessionID != a.SessionID {
		t.Fatal("lost the session")
	}
}

func pendingFor(t *testing.T, f *Fleet, agentID string) model.Approval {
	t.Helper()
	var out model.Approval
	eventually(t, "a pending approval", func() bool {
		aps, _ := f.Approvals(model.ApprovalPending)
		for _, ap := range aps {
			if ap.AgentID == agentID {
				out = ap
				return true
			}
		}
		return false
	})
	return out
}

func TestMailReachesTheAgentInBothModes(t *testing.T) {
	quickSwitch(t)
	e := newEnv(t)
	f := e.started()
	a, chat := talkingAgent(t, e, f, "w")
	other, _ := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "o"})
	from := AgentCaller(other.ID)

	send := func(body string) {
		t.Helper()
		if _, err := f.SendMessage(from, SendRequest{To: "w", Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	send("one")
	eventually(t, "chat mail", func() bool { s := chat.Sent(); return len(s) > 0 && strings.Contains(s[len(s)-1], "one") })
	chat.Turn("got one", 0, 1)
	waitState(t, f, "w", model.StateIdle)

	if _, err := f.SetMode(OwnerCaller, "w", ModeRequest{Mode: model.ModeTerminal}); err != nil {
		t.Fatal(err)
	}
	tui := newProc(t, e.term, a.ID, nil)
	tui.State(model.StateIdle)
	send("two")
	eventually(t, "typed mail", func() bool {
		s := tui.Sent()
		return len(s) == 1 && strings.Contains(s[0], "[message from o (worker)]\n│ two")
	})
	if !hasEntry(t, f, "w", model.EntryMessageIn, "two") {
		t.Fatal("typed mail missing from the timeline")
	}
	tui.State(model.StateIdle) // (the fleet marked it working when it typed)
	waitState(t, f, "w", model.StateIdle)

	if _, err := f.SetMode(OwnerCaller, "w", ModeRequest{Mode: model.ModeChat}); err != nil {
		t.Fatal(err)
	}
	back := newProc(t, e.chat, a.ID, chat)
	back.State(model.StateIdle)
	send("three")
	eventually(t, "chat mail again", func() bool { s := back.Sent(); return len(s) == 1 && strings.Contains(s[0], "three") })
}

func TestDaemonRestartBringsASwitchedAgentHome(t *testing.T) {
	quickSwitch(t)
	e := newEnv(t)
	f := e.started()
	a, _ := talkingAgent(t, e, f, "w")
	if _, err := f.SetMode(OwnerCaller, "w", ModeRequest{Mode: model.ModeTerminal}); err != nil {
		t.Fatal(err)
	}
	tui := newProc(t, e.term, a.ID, nil)
	tui.State(model.StateIdle)
	waitState(t, f, "w", model.StateIdle)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.Close(ctx); err != nil {
		t.Fatal(err)
	}

	before := len(e.chat.Procs())
	f2 := e.started()
	got := mustAgent(t, f2, "w")
	if got.Mode != model.ModeChat || got.HomeMode != "" {
		t.Fatalf("after a restart: %+v", got)
	}
	var p *fake.Process
	eventually(t, "started in chat", func() bool {
		for _, x := range e.chat.Procs()[before:] {
			if x.Spec.AgentID == a.ID {
				p = x
				return true
			}
		}
		return false
	})
	if p.Spec.SessionID != a.SessionID {
		t.Fatal("lost the session")
	}
}

func TestScreenClosedByItselfGoesBackToChat(t *testing.T) {
	quickSwitch(t)
	e := newEnv(t)
	f := e.started()
	a, chat := talkingAgent(t, e, f, "w")
	if _, err := f.SetMode(OwnerCaller, "w", ModeRequest{Mode: model.ModeTerminal}); err != nil {
		t.Fatal(err)
	}
	tui := newProc(t, e.term, a.ID, nil)
	tui.State(model.StateIdle)
	tui.Exit(0, nil) // the owner typed /exit
	back := newProc(t, e.chat, a.ID, chat)
	if back.Spec.SessionID != a.SessionID || mustAgent(t, f, "w").Mode != model.ModeChat {
		t.Fatal("not back in chat on the same conversation")
	}
	if !hasEntry(t, f, "w", model.EntrySystem, "Claude Code's screen closed; back in chat") {
		t.Fatal("no note")
	}

	// An agent launched in its screen stays exited, as before.
	ct, _ := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "ct", Mode: model.ModeTerminal})
	p := e.term.Last(ct.ID)
	p.Exit(0, nil)
	waitState(t, f, "ct", model.StateExited)
	if got := mustAgent(t, f, "ct"); got.Mode != model.ModeTerminal {
		t.Fatalf("%+v", got)
	}
}

func TestSetModeRules(t *testing.T) {
	quickSwitch(t)
	e := newUnguardedEnv(t)
	f := e.started()
	sh, _ := f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "sh", Harness: model.HarnessShell})
	if _, err := f.SetMode(OwnerCaller, "sh", ModeRequest{Mode: model.ModeChat}); KindOf(err) != KindInvalid {
		t.Fatalf("a shell in chat mode: %v", err)
	}
	if _, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "pi", Harness: model.HarnessPi}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.SetMode(OwnerCaller, "pi", ModeRequest{Mode: model.ModeChat}); KindOf(err) != KindInvalid {
		t.Fatalf("pi in chat mode: %v", err)
	}
	if _, err := f.SetMode(OwnerCaller, "sh", ModeRequest{Mode: "tui"}); KindOf(err) != KindInvalid {
		t.Fatal("unknown mode")
	}
	head := mustAgent(t, f, "head")
	if _, err := f.SetMode(AgentCaller(head.ID), sh.Name, ModeRequest{Mode: model.ModeTerminal}); KindOf(err) != KindForbidden {
		t.Fatalf("an agent switched a mode: %v", err)
	}
	if res, err := f.SetMode(OwnerCaller, "sh", ModeRequest{Mode: model.ModeTerminal}); err != nil || !strings.Contains(res.Note, "already") {
		t.Fatalf("same mode: %+v %v", res, err)
	}

	// A stopped agent only changes the mode it starts in.
	a, _ := talkingAgent(t, e, f, "w")
	if _, err := f.StopAgent(OwnerCaller, "w"); err != nil {
		t.Fatal(err)
	}
	res, err := f.SetMode(OwnerCaller, "w", ModeRequest{Mode: model.ModeTerminal})
	if err != nil || res.Pending || res.Agent.State != model.StateStopped || e.term.Last(a.ID) != nil {
		t.Fatalf("stopped: %+v %v", res, err)
	}
	if _, err := f.StartAgent(OwnerCaller, "w"); err != nil {
		t.Fatal(err)
	}
	if p := newProc(t, e.term, a.ID, nil); p.Spec.SessionID != a.SessionID {
		t.Fatal("started in the screen without the conversation")
	}

	// Codex: thread/resume in chat, `codex resume ID` in its screen.
	cx, _ := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "cx", Harness: model.HarnessCodex})
	cp := e.chat.Last(cx.ID)
	cp.Emit(runner.Event{Kind: runner.EvSession, SessionID: "thread-7"})
	cp.State(model.StateIdle)
	eventually(t, "codex thread", func() bool { return mustAgent(t, f, "cx").SessionID == "thread-7" })
	waitState(t, f, "cx", model.StateIdle)
	if _, err := f.SetMode(OwnerCaller, "cx", ModeRequest{Mode: model.ModeTerminal}); err != nil {
		t.Fatal(err)
	}
	ct := newProc(t, e.term, cx.ID, nil)
	s := ct.Spec
	s.Command = "/bin/true"
	cmd, err := harness.TerminalCommand(s)
	if err != nil || len(cmd.Args) < 2 || cmd.Args[0] != "resume" || cmd.Args[1] != "thread-7" || s.MCP == nil {
		t.Fatalf("codex screen %v %v %+v", cmd.Args, err, s)
	}
}

func TestFailedSwitchFallsBack(t *testing.T) {
	quickSwitch(t)
	e := newEnv(t)
	f := e.started()
	a, chat := talkingAgent(t, e, f, "w")
	e.term.FailStarts(errors.New("no pty"))
	if _, err := f.SetMode(OwnerCaller, "w", ModeRequest{Mode: model.ModeTerminal}); err != nil {
		t.Fatal(err)
	}
	back := newProc(t, e.chat, a.ID, chat)
	if back.Spec.SessionID != a.SessionID {
		t.Fatal("lost the session")
	}
	got := mustAgent(t, f, "w")
	if got.Mode != model.ModeChat || got.HomeMode != "" {
		t.Fatalf("%+v", got)
	}
	if !hasEntry(t, f, "w", model.EntryError, "Could not switch") {
		t.Fatal("no error note")
	}
}

func TestRestartAppliesAPendingSwitch(t *testing.T) {
	quickSwitch(t)
	e := newEnv(t)
	f := e.started()
	a, chat := talkingAgent(t, e, f, "w")
	chat.State(model.StateWorking)
	waitState(t, f, "w", model.StateWorking)
	if res, _ := f.SetMode(OwnerCaller, "w", ModeRequest{Mode: model.ModeTerminal}); !res.Pending {
		t.Fatal("not pending")
	}
	// The owner restarts it anyway (that ends the turn): it comes back in its screen.
	if _, err := f.RestartAgent(OwnerCaller, "w"); err != nil {
		t.Fatal(err)
	}
	newProc(t, e.term, a.ID, nil)
	if got := mustAgent(t, f, "w"); got.Mode != model.ModeTerminal || got.PendingMode != "" {
		t.Fatalf("%+v", got)
	}
}

func TestSwitchWaitsUntilTheAgentSettled(t *testing.T) {
	old := switchSettle
	switchSettle = 300 * time.Millisecond
	t.Cleanup(func() { switchSettle = old })
	e := newEnv(t)
	f := e.started()
	a, chat := talkingAgent(t, e, f, "w")
	chat.State(model.StateWorking)
	waitState(t, f, "w", model.StateWorking)
	chat.State(model.StateIdle) // the turn ended just now
	waitState(t, f, "w", model.StateIdle)
	if _, err := f.SetMode(OwnerCaller, "w", ModeRequest{Mode: model.ModeTerminal}); err != nil {
		t.Fatal(err)
	}
	// claude picks up a message it queued during the turn: that turn is not cut off
	time.Sleep(100 * time.Millisecond)
	chat.State(model.StateWorking)
	time.Sleep(400 * time.Millisecond)
	if e.term.Last(a.ID) != nil {
		t.Fatal("switched during a turn that began while settling")
	}
	if mustAgent(t, f, "w").PendingMode != model.ModeTerminal {
		t.Fatal("the switch was dropped")
	}
	chat.State(model.StateIdle)
	start := time.Now()
	newProc(t, e.term, a.ID, nil)
	if time.Since(start) < 250*time.Millisecond {
		t.Fatal("did not settle")
	}
}

// The head in its own screen: owner messages in a topic are typed in with their topic line,
// what the screen's transcript reports fills the card, the screen's end of turn closes it, and
// a message typed by hand into the screen starts a General card.
func TestHeadBoardWhileInItsScreen(t *testing.T) {
	quickSwitch(t)
	e := newEnv(t)
	f := e.started()
	head := mustAgent(t, f, "head")
	hp := e.chat.Last(head.ID)
	hp.Emit(runner.Event{Kind: runner.EvSession, SessionID: head.SessionID})
	hp.State(model.StateIdle)
	waitState(t, f, "head", model.StateIdle)
	if _, err := f.SetMode(OwnerCaller, "head", ModeRequest{Mode: model.ModeTerminal}); err != nil {
		t.Fatal(err)
	}
	tui := newProc(t, e.term, head.ID, nil)
	tui.State(model.StateIdle)
	waitState(t, f, "head", model.StateIdle)

	topic, err := f.CreateThread("Pricing", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.SendOwner("head", "draft the pricing table", topic.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "typed with its topic", func() bool { return lastSent(tui) == "[topic: Pricing]\n[message from owner] draft the pricing table" })
	tui.State(model.StateWorking)
	tui.Entry(model.EntryAssistant, "Here is the table.")
	tui.Emit(runner.Event{Kind: runner.EvTimeline, Entry: &model.TimelineEntry{Kind: model.EntryResult, Meta: map[string]any{"source": "screen"}}})
	tui.State(model.StateIdle)
	waitState(t, f, "head", model.StateIdle)

	tui.Emit(runner.Event{Kind: runner.EvTimeline, Entry: &model.TimelineEntry{Kind: model.EntryUser, Text: "and by hand",
		Meta: map[string]any{"from": model.Owner, "source": "screen"}}})
	tui.State(model.StateWorking)
	tui.Entry(model.EntryAssistant, "Sure.")
	tui.Emit(runner.Event{Kind: runner.EvTimeline, Entry: &model.TimelineEntry{Kind: model.EntryResult, Meta: map[string]any{"source": "screen"}}})
	// Wait for the full expected state, not just for the cards to exist: the General card is
	// created (with its reply) before the result event marks it done.
	var pricing, general *model.BoardCard
	settled := func() bool {
		pricing, general = nil, nil
		b := board(t, f)
		for i := range b.Cards {
			switch b.Cards[i].ThreadID {
			case topic.ID:
				pricing = &b.Cards[i]
			case model.GeneralThread:
				general = &b.Cards[i]
			}
		}
		return len(cardsIn(b, model.GeneralThread)) == 1 &&
			pricing != nil && pricing.Done && pricing.Reply == "Here is the table." && pricing.Kind == "owner" &&
			general != nil && general.Done && general.Prompt == "and by hand" && general.Reply == "Sure." && general.Kind == "owner"
	}
	deadline := time.Now().Add(3 * time.Second)
	for !settled() {
		if time.Now().After(deadline) {
			t.Fatalf("cards never settled: topic card %+v; general card %+v", pricing, general)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
