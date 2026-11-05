package fleet

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

func TestStartupMigratesExistingHeadHarnessWithoutLosingHistoryOrMail(t *testing.T) {
	e := newUnguardedEnv(t)
	t.Setenv("TMX_HEAD_HARNESS", "")
	created := model.Now()
	head := model.Agent{ID: "existing-head-id", Name: HeadName, Level: model.LevelHead,
		Harness: model.HarnessClaude, Mode: model.ModeChat, Model: sonnet, Cwd: e.workspace,
		State: model.StateStopped, Desired: "running", SessionID: "claude-session-id", CreatedAt: created}
	if err := e.st.PutAgent(head); err != nil {
		t.Fatal(err)
	}
	entry := model.TimelineEntry{AgentID: head.ID, Seq: 1, Kind: model.EntryUser, Text: "keep this history", At: created}
	thread := model.Thread{ID: "topic-1", Title: "Migration topic", Kind: model.ThreadTopic,
		CreatedAt: created, UpdatedAt: created}
	if err := e.st.PutThread(thread); err != nil {
		t.Fatal(err)
	}
	entry.ThreadID = thread.ID
	if err := e.st.AppendTimeline(entry); err != nil {
		t.Fatal(err)
	}
	mail := model.Message{ID: "queued-before-migration", From: model.Owner, To: head.ID,
		Body: "deliver after migration", State: model.MsgQueued, CreatedAt: created}
	if err := e.st.PutMessage(mail); err != nil {
		t.Fatal(err)
	}

	t.Setenv("TMX_HEAD_HARNESS", string(model.HarnessCodex))
	f := e.fleet()
	if err := f.Start(); err != nil {
		t.Fatal(err)
	}
	proc := e.chat.Last(head.ID)
	proc.Emit(runner.Event{Kind: runner.EvSession, SessionID: "new-codex-thread"})
	proc.State(model.StateIdle)

	eventually(t, "new Codex thread persisted", func() bool { return mustAgent(t, f, HeadName).SessionID == "new-codex-thread" })
	got := mustAgent(t, f, HeadName)
	if got.ID != head.ID || got.CreatedAt != head.CreatedAt || got.Harness != model.HarnessCodex ||
		got.Model != "" || got.SessionID != "new-codex-thread" {
		t.Fatalf("migrated head identity/session: before=%+v after=%+v", head, got)
	}
	if spec := proc.Spec; spec.Harness != model.HarnessCodex || spec.SessionID != "" {
		t.Fatalf("Codex received old Claude session: %+v", spec)
	}
	entries, err := e.st.Timeline(head.ID, 0, 10)
	keptEntry := false
	for _, gotEntry := range entries {
		if gotEntry.Seq == entry.Seq && gotEntry.Text == entry.Text && gotEntry.ThreadID == thread.ID {
			keptEntry = true
		}
	}
	if err != nil || !keptEntry {
		t.Fatalf("timeline after migration: %+v %v", entries, err)
	}
	board, err := f.Board()
	keptCard := false
	for _, card := range board.Cards {
		if card.ID == entry.Seq && card.ThreadID == thread.ID {
			keptCard = true
		}
	}
	if err != nil || board.HeadID != head.ID || !keptCard {
		t.Fatalf("board continuity after migration: %+v %v", board, err)
	}
	eventually(t, "queued mail delivered once", func() bool {
		m, err := e.st.GetMessage(mail.ID)
		return err == nil && m.State == model.MsgDelivered
	})
	eventually(t, "Codex received queued message", func() bool { return len(e.chat.Last(head.ID).Sent()) == 1 })
	if sent := e.chat.Last(head.ID).Sent(); len(sent) != 1 || !strings.Contains(sent[0], mail.Body) {
		t.Fatalf("delivered mail: %q", sent)
	}
}

func TestStartupMigrationPreservesStoppedHeadQueueAndSameHarnessSession(t *testing.T) {
	for _, tc := range []struct {
		name    string
		harness model.Harness
		model   string
		session string
	}{
		{name: "stopped head", harness: model.HarnessClaude, model: sonnet},
		{name: "same harness", harness: model.HarnessCodex, model: "gpt-6", session: "codex-thread"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newUnguardedEnv(t)
			head := model.Agent{ID: "preserved-head", Name: HeadName, Level: model.LevelHead,
				Harness: tc.harness, Mode: model.ModeChat, Model: tc.model, State: model.StateStopped,
				Desired: "stopped", SessionID: tc.session, CreatedAt: model.Now()}
			if err := e.st.PutAgent(head); err != nil {
				t.Fatal(err)
			}
			mail := model.Message{ID: "must-stay-queued", From: model.Owner, To: head.ID,
				Body: "do not deliver", State: model.MsgQueued, CreatedAt: head.CreatedAt}
			if err := e.st.PutMessage(mail); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TMX_HEAD_HARNESS", string(model.HarnessCodex))
			f := e.fleet()
			if err := f.Start(); err != nil {
				t.Fatal(err)
			}
			got := mustAgent(t, f, HeadName)
			wantModel, wantSession := head.Model, head.SessionID
			if head.Harness != model.HarnessCodex {
				wantModel, wantSession = "", ""
			}
			if got.ID != head.ID || got.Harness != model.HarnessCodex || got.Model != wantModel || got.SessionID != wantSession || got.Desired != "stopped" {
				t.Fatalf("unchanged/stopped head: before=%+v after=%+v", head, got)
			}
			queued, err := e.st.ListMessages(store.MessageFilter{To: head.ID, State: model.MsgQueued})
			if err != nil || len(queued) != 1 || queued[0] != mail {
				t.Fatalf("queued mail changed: %+v %v", queued, err)
			}
		})
	}
}

func TestStartupLeavesExistingHeadHarnessWhenUnsetOrInvalid(t *testing.T) {
	for _, value := range []string{"", "shell"} {
		// Not t.Run(""): Go names that subtest "#00", and store.Open's file: URI ends at a '#', so the
		// database landed in one file shared by every run in the temp folder.
		name := value
		if name == "" {
			name = "unset"
		}
		t.Run(name, func(t *testing.T) {
			e := newUnguardedEnv(t)
			head := model.Agent{ID: "existing-codex-head", Name: HeadName, Level: model.LevelHead,
				Harness: model.HarnessCodex, Mode: model.ModeChat, State: model.StateStopped,
				Desired: "stopped", CreatedAt: model.Now()}
			if err := e.st.PutAgent(head); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TMX_HEAD_HARNESS", value)
			f := e.fleet()
			if err := f.Start(); err != nil {
				t.Fatal(err)
			}
			if got := mustAgent(t, f, HeadName); got.ID != head.ID || got.Harness != model.HarnessCodex {
				t.Fatalf("head changed without a valid override: %+v", got)
			}
		})
	}
}

func TestLaunchHarnessesAndModes(t *testing.T) {
	e := newUnguardedEnv(t)
	f := e.started()
	cx, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "cx", Harness: model.HarnessCodex})
	if err != nil || cx.Mode != model.ModeChat || cx.Model != "" {
		t.Fatalf("codex: %+v %v", cx, err)
	}
	spec := e.chat.Last(cx.ID).Spec
	if spec.MCP == nil || spec.MCP.Command != "/opt/tmx/tmx" || !slices.Contains(spec.MCP.EnvKeys, EnvToken) ||
		spec.SystemPrompt == "" || spec.PermissionMode != "auto" || spec.MCPConfigPath != "" {
		t.Fatalf("codex spec %+v", spec)
	}
	pi, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "pi1", Harness: model.HarnessPi})
	if err != nil || pi.Mode != model.ModeTerminal || pi.SessionID == "" || e.term.Last(pi.ID).Spec.SessionID != pi.SessionID {
		t.Fatalf("pi: %+v %v", pi, err)
	}
	oc, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "oc1", Harness: model.HarnessOpenCode})
	if err != nil || oc.Mode != model.ModeTerminal || !strings.HasPrefix(oc.SessionID, "ses") ||
		e.term.Last(oc.ID).Spec.SessionID != oc.SessionID || e.term.Last(oc.ID).Spec.Model != "" {
		t.Fatalf("opencode: %+v %v", oc, err)
	}
	if _, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "oc", Harness: model.HarnessOpenCode, Mode: model.ModeChat}); KindOf(err) != KindInvalid {
		t.Fatalf("opencode in chat mode: %v", err)
	}
	if _, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "x", Harness: "vim"}); KindOf(err) != KindInvalid {
		t.Fatal("unknown harness")
	}
	// A profile's models are Claude's: the owner names any model for another harness, an
	// agent only one its profile allows.
	if a, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "cx2", Harness: model.HarnessCodex, Model: "gpt-6"}); err != nil || a.Model != "gpt-6" {
		t.Fatalf("owner's model for codex: %+v %v", a, err)
	}
	head := mustAgent(t, f, "head")
	hc := callerFor(t, f, e.chat, head.ID)
	if _, err := f.LaunchAgent(hc, LaunchRequest{Name: "cx3", Harness: model.HarnessCodex, Model: "gpt-6"}); KindOf(err) != KindForbidden {
		t.Fatalf("an agent's model outside the profile: %v", err)
	}
	// A read-only profile: Codex in its read-only sandbox; refused on pi (no permission controls).
	ro, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "cx-ro", Harness: model.HarnessCodex, Profile: "reviewer"})
	if err != nil || e.chat.Last(ro.ID).Spec.PermissionMode != "plan" {
		t.Fatalf("read-only codex: %v", err)
	}
	if _, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "pi-ro", Harness: model.HarnessPi, Profile: "reviewer"}); KindOf(err) != KindInvalid {
		t.Fatalf("read-only pi: %v", err)
	}
	// Claude in terminal mode gets its conversation id up front, like chat mode.
	ct, _ := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "ct", Mode: model.ModeTerminal})
	if args := e.term.Last(ct.ID).Spec.ExtraArgs; ct.SessionID == "" || !slices.Equal(args, []string{"--session-id", ct.SessionID}) {
		t.Fatalf("claude terminal: %v %q", args, ct.SessionID)
	}
}

func TestInstructionsGoInFrontForHarnessesWithoutSystemPrompt(t *testing.T) {
	e := newUnguardedEnv(t)
	f := e.started()
	oc, _ := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "oc", Harness: model.HarnessOpenCode, Prompt: "fix the bug"})
	sent := e.term.Last(oc.ID).Sent()
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "[tmx instructions]\n") || !strings.HasSuffix(sent[0], "\n\nfix the bug") {
		t.Fatalf("%q", sent)
	}
	cx, _ := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "cx", Harness: model.HarnessCodex, Prompt: "fix it"})
	if sent := e.chat.Last(cx.ID).Sent(); len(sent) != 1 || sent[0] != "fix it" {
		t.Fatalf("%q", sent)
	}
}

func TestTerminalMailWaitsForIdleAndQuietTyping(t *testing.T) {
	e := newUnguardedEnv(t)
	f := e.started()
	a, _ := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "tui", Harness: model.HarnessClaude, Mode: model.ModeTerminal})
	p := e.term.Last(a.ID)
	p.State(model.StateWorking)
	eventually(t, "working", func() bool { x, _ := f.Agent("tui"); return x.State == model.StateWorking })
	if _, err := f.SendOwner("tui", "hello", ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if len(p.Sent()) != 0 {
		t.Fatal("typed into a busy terminal")
	}
	p.State(model.StateIdle)
	eventually(t, "delivered when idle", func() bool { return len(p.Sent()) == 1 })
	if !strings.Contains(p.Sent()[0], "hello") {
		t.Fatal(p.Sent())
	}
	// a human typed a moment ago: wait
	p.State(model.StateWorking)
	eventually(t, "working", func() bool { x, _ := f.Agent("tui"); return x.State == model.StateWorking })
	_ = f.TerminalInput(OwnerCaller, "tui", []byte("draft"))
	p.State(model.StateIdle)
	_, _ = f.SendOwner("tui", "second", "")
	time.Sleep(100 * time.Millisecond)
	if len(p.Sent()) != 1 {
		t.Fatal("typed over a human")
	}
	f.mu.Lock()
	f.agents[a.ID].humanInputAt = time.Now().Add(-TypingQuiet)
	f.mu.Unlock()
	f.tryDeliver(a.ID)
	eventually(t, "delivered after the quiet", func() bool { return len(p.Sent()) == 2 })

	// a shell never gets agent mail typed into it
	sh, _ := f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "sh", Harness: model.HarnessShell})
	sp := e.term.Last(sh.ID)
	sp.State(model.StateIdle)
	w, _ := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "w"})
	if _, err := f.SendMessage(AgentCaller(w.ID), SendRequest{To: "sh", Body: "rm -rf /"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if len(sp.Sent()) != 0 {
		t.Fatal("mail typed into a shell")
	}
}

func TestTerminalMenuApprovalIsAnsweredThroughTheRunner(t *testing.T) {
	e := newUnguardedEnv(t)
	f := e.started()
	a, _ := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "tui", Harness: model.HarnessCodex, Mode: model.ModeTerminal})
	p := e.term.Last(a.ID)
	p.Emit(runner.Event{Kind: runner.EvApproval, Approval: &runner.ApprovalRequest{
		ID: "menu-1", Kind: model.ApprovalQuestion, Tool: model.ToolTerminalMenu, Question: "Pick a plan",
		Options: []string{"Small", "Large"}, Input: json.RawMessage(`{"harness":"codex"}`)}})
	var ap model.Approval
	eventually(t, "the approval", func() bool {
		list, _ := f.Approvals(model.ApprovalPending)
		if len(list) == 1 {
			ap = list[0]
		}
		return len(list) == 1
	})
	if ap.Kind != model.ApprovalQuestion || ap.Question != "Pick a plan" || len(ap.Options) != 2 {
		t.Fatalf("%+v", ap)
	}
	if x, _ := f.Agent("tui"); x.State != model.StateWaiting {
		t.Fatal(x.State)
	}
	if _, err := f.Decide(OwnerCaller, ap.ID, DecideRequest{Decision: "answer", Answer: "Large"}); err != nil {
		t.Fatal(err)
	}
	if ans := p.Answers(); len(ans) != 1 || ans[0].RequestID != "menu-1" || ans[0].Decision.Answer != "Large" || !ans[0].Decision.Allow {
		t.Fatalf("%+v", ans)
	}
	got, _ := f.st.GetApproval(ap.ID)
	if got.State != model.ApprovalAnswered || got.Answer != "Large" {
		t.Fatalf("%+v", got)
	}
	// the harness answered it itself (human in the terminal): withdrawn
	p.Emit(runner.Event{Kind: runner.EvApproval, Approval: &runner.ApprovalRequest{ID: "menu-2", Tool: model.ToolTerminalMenu, Options: []string{"Yes", "No"}}})
	eventually(t, "second", func() bool { l, _ := f.Approvals(model.ApprovalPending); return len(l) == 1 })
	l, _ := f.Approvals(model.ApprovalPending)
	if _, err := f.Decide(OwnerCaller, l[0].ID, DecideRequest{Decision: "answer"}); KindOf(err) != KindInvalid {
		t.Fatal("an empty answer")
	}
	p.Emit(runner.Event{Kind: runner.EvApprovalCancelled, Approval: &runner.ApprovalRequest{ID: "menu-2"}})
	eventually(t, "withdrawn", func() bool { l, _ := f.Approvals(model.ApprovalPending); return len(l) == 0 })
	// a permission without options still takes only allow / deny
	p.Emit(runner.Event{Kind: runner.EvApproval, Approval: &runner.ApprovalRequest{ID: "7", Tool: "Bash"}})
	eventually(t, "third", func() bool { l, _ := f.Approvals(model.ApprovalPending); return len(l) == 1 })
	l, _ = f.Approvals(model.ApprovalPending)
	if _, err := f.Decide(OwnerCaller, l[0].ID, DecideRequest{Decision: "answer", Answer: "x"}); KindOf(err) != KindInvalid {
		t.Fatal("answered a plain permission")
	}
	if _, err := f.Decide(OwnerCaller, l[0].ID, DecideRequest{Decision: "allow"}); err != nil {
		t.Fatal(err)
	}
}

// The head's harness comes from TMX_HEAD_HARNESS, else config.json "head_harness"; nothing set
// (or something that is not a chat harness) leaves an existing head as it is.
func TestConfiguredHeadHarness(t *testing.T) {
	e := newUnguardedEnv(t)
	f := e.fleet()
	for _, c := range []struct {
		env, cfg string
		want     model.Harness
		ok       bool
	}{
		{"", "", "", false},
		{"", "codex", model.HarnessCodex, true},
		{"", "claude", model.HarnessClaude, true},
		{"claude", "codex", model.HarnessClaude, true},
		{"", "shell", "", false},
		{"shell", "codex", "", false},
	} {
		t.Setenv("TMX_HEAD_HARNESS", c.env)
		f.opts.HeadHarness = c.cfg
		if got, ok := f.configuredHeadHarness(); got != c.want || ok != c.ok {
			t.Errorf("env %q config %q: %q %v, want %q %v", c.env, c.cfg, got, ok, c.want, c.ok)
		}
	}
}
