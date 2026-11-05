package fleet

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/config"
	"github.com/blackcode-ch/tmx-go/internal/guard"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/runner/fake"
	"github.com/blackcode-ch/tmx-go/internal/store"
)

const (
	opus   = "claude-opus-5-5"
	sonnet = "claude-sonnet-5"
)

type env struct {
	t         *testing.T
	home      string
	workspace string
	st        *store.Store
	chat      *fake.Runner
	term      *fake.Runner
	guard     *guard.Settings
	bypass    *bool // Options.AgentBypass (nil = the default, on)
	accounts  AccountHomes
	// hostExe and termRunner: session hosts (hosting_test.go); nil/"" = none, e.term.
	hostExe    string
	termRunner runner.Runner
	chatRunner runner.Runner
}

// Explicit owner opt-out for tests of harness behavior unrelated to the guard.
// Production defaults and admission tests continue to use the enabled guard.
func newUnguardedEnv(t *testing.T) *env {
	old := launchGOOS
	launchGOOS = "linux" // fake runner: test harness semantics, not Windows host capability
	t.Cleanup(func() { launchGOOS = old })
	e := newEnv(t)
	e.guard = &guard.Settings{Enabled: false}
	return e
}

func newEnv(t *testing.T) *env {
	t.Helper()
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	p := config.Paths{Home: home}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &env{t: t, home: home, workspace: filepath.Join(t.TempDir(), "Projects"), st: st,
		chat: fake.New(), term: fake.New()}
}

// fleet builds a Fleet on the env's store (a second call simulates a daemon restart).
func (e *env) fleet() *Fleet {
	e.t.Helper()
	var term, chat runner.Runner = e.term, e.chat
	if e.termRunner != nil {
		term = e.termRunner
	}
	if e.chatRunner != nil {
		chat = e.chatRunner
	}
	f, err := New(Options{Elevated: func() bool { return false },
		Store: e.st, Paths: config.Paths{Home: e.home}, HostExecutable: e.hostExe,
		Runners: map[model.Mode]runner.Runner{model.ModeChat: chat, model.ModeTerminal: term},
		Machine: "testbox", Version: "test", Workspace: e.workspace, URL: "http://127.0.0.1:1",
		Executable:  "/opt/tmx/tmx",
		Guard:       e.guard,
		AgentBypass: e.bypass,
		Accounts:    e.accounts,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		f.Close(ctx)
	})
	return f
}

func (e *env) started() *Fleet {
	e.t.Helper()
	f := e.fleet()
	if err := f.Start(); err != nil {
		e.t.Fatal(err)
	}
	return f
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func mustAgent(t *testing.T, f *Fleet, name string) model.Agent {
	t.Helper()
	a, err := f.Agent(name)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// Tests of unrelated worker behavior still launch through a valid project/lead.
func workerProject(t *testing.T, f *Fleet) string {
	t.Helper()
	f.mu.Lock()
	for _, p := range f.projects {
		if p.LeadID != "" && f.agents[p.LeadID] != nil {
			f.mu.Unlock()
			return p.ID
		}
	}
	f.mu.Unlock()
	p, err := f.CreateProject(OwnerCaller, ProjectRequest{Name: "Test workers"})
	if err != nil {
		t.Fatal(err)
	}
	// Keep queue/capacity scenarios focused on the worker under test.
	if _, err := f.StopAgent(OwnerCaller, p.LeadID); err != nil {
		t.Fatal(err)
	}
	return p.ID
}

func waitState(t *testing.T, f *Fleet, name string, st model.AgentState) {
	t.Helper()
	eventually(t, name+" state "+string(st), func() bool { return mustAgent(t, f, name).State == st })
}

func callerFor(t *testing.T, f *Fleet, r *fake.Runner, id string) Caller {
	t.Helper()
	p := r.Last(id)
	for _, kv := range p.Spec.Env {
		if tok, ok := strings.CutPrefix(kv, EnvToken+"="); ok {
			c, ok := f.CallerForToken(tok)
			if !ok || c.AgentID != id {
				t.Fatalf("token does not map to %s", id)
			}
			return c
		}
	}
	t.Fatal("no token in env")
	return Caller{}
}

func TestHeadBootstrap(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	snap, err := f.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snap.Head == nil || snap.Head.Name != "head" || snap.Head.Level != model.LevelHead {
		t.Fatalf("head: %+v", snap.Head)
	}
	h := *snap.Head
	if h.Model != opus || h.Cwd != e.workspace || h.Desired != "running" {
		t.Fatalf("head fields: %+v", h)
	}
	if _, err := os.Stat(e.workspace); err != nil {
		t.Fatal("workspace not created")
	}
	p := e.chat.Last(h.ID)
	if p == nil {
		t.Fatal("head not started")
	}
	if p.Spec.Model != opus || !slices.Equal(p.Spec.AllowedTools, []string{"mcp__tmxg"}) ||
		!strings.Contains(p.Spec.SystemPrompt, "head agent") || p.Spec.PermissionMode != "bypassPermissions" { // guarded: bypass (guard.go)
		t.Fatalf("spec: %+v", p.Spec)
	}
	// New conversation: id assigned up front, not resumed.
	if p.Spec.SessionID != "" || len(p.Spec.ExtraArgs) != 2 || p.Spec.ExtraArgs[0] != "--session-id" {
		t.Fatalf("session args: %+v", p.Spec)
	}
	if got := mustAgent(t, f, "head").SessionID; got != p.Spec.ExtraArgs[1] || len(got) != 36 {
		t.Fatalf("pre-assigned session %q vs %q", got, p.Spec.ExtraArgs[1])
	}
	// mcp.json points at the tmx binary with the agent's env.
	var cfg struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	b, err := os.ReadFile(p.Spec.MCPConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(b, &cfg)
	srv := cfg.MCPServers["tmxg"]
	if srv.Command != "/opt/tmx/tmx" || !slices.Equal(srv.Args, []string{"mcp"}) ||
		srv.Env[EnvAgent] != h.ID || srv.Env[EnvURL] != "http://127.0.0.1:1" || len(srv.Env[EnvToken]) != 64 {
		t.Fatalf("mcp.json: %s", b)
	}
	if _, ok := f.CallerForToken(srv.Env[EnvToken]); !ok {
		t.Fatal("mcp.json token unknown")
	}

	// A restarted daemon keeps exactly one head.
	f2 := e.started()
	snap2, _ := f2.Snapshot()
	heads := 0
	for _, a := range snap2.Agents {
		if a.Level == model.LevelHead {
			heads++
		}
	}
	if heads != 1 || snap2.Head.ID != h.ID {
		t.Fatalf("heads after restart: %d", heads)
	}
}

func TestProjectCreateLaunchesLeadOnOpus(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	head := mustAgent(t, f, "head")
	p, err := f.CreateProject(OwnerCaller, ProjectRequest{Name: "My App!", Brief: "build a todo app"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Slug != "my-app" || p.Folder != filepath.Join(e.workspace, "my-app") || p.LeadID == "" {
		t.Fatalf("project: %+v", p)
	}
	if st, err := os.Stat(p.Folder); err != nil || !st.IsDir() {
		t.Fatal("folder not created")
	}
	lead := mustAgent(t, f, "my-app-lead")
	if lead.ID != p.LeadID || lead.Level != model.LevelLead || lead.Model != opus ||
		lead.ParentID != head.ID || lead.ProjectID != p.ID || lead.Cwd != p.Folder {
		t.Fatalf("lead: %+v", lead)
	}
	proc := e.chat.Last(lead.ID)
	if proc == nil || proc.Spec.Model != opus || proc.Spec.Cwd != p.Folder {
		t.Fatalf("lead process: %+v", proc)
	}
	sent := proc.Sent()
	if len(sent) != 1 || !strings.Contains(sent[0], "build a todo app") {
		t.Fatalf("kickoff: %q", sent)
	}
	if !strings.Contains(proc.Spec.SystemPrompt, "lead of project") {
		t.Fatalf("lead prompt: %s", proc.Spec.SystemPrompt)
	}
	if _, err := f.CreateProject(OwnerCaller, ProjectRequest{Name: "my app"}); KindOf(err) != KindConflict {
		t.Fatalf("duplicate project: %v", err)
	}

	// The head may create projects; a lead may not.
	hc := callerFor(t, f, e.chat, head.ID)
	if _, err := f.CreateProject(hc, ProjectRequest{Name: "second"}); err != nil {
		t.Fatalf("head create: %v", err)
	}
	lc := callerFor(t, f, e.chat, lead.ID)
	if _, err := f.CreateProject(lc, ProjectRequest{Name: "third"}); KindOf(err) != KindForbidden {
		t.Fatalf("lead create: %v", err)
	}
}

func TestProjectCreateSelectsLeadHarness(t *testing.T) {
	for _, tc := range []struct {
		name    string
		harness model.Harness
		want    model.Harness
		model   string
	}{
		{name: "default", want: model.HarnessClaude, model: opus},
		{name: "claude", harness: model.HarnessClaude, want: model.HarnessClaude, model: opus},
		{name: "codex", harness: model.HarnessCodex, want: model.HarnessCodex, model: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newUnguardedEnv(t)
			f := e.started()
			p, err := f.CreateProject(OwnerCaller, ProjectRequest{Name: "harness-choice", Harness: tc.harness})
			if err != nil {
				t.Fatal(err)
			}
			lead := mustAgent(t, f, "harness-choice-lead")
			if lead.Harness != tc.want || lead.Model != tc.model || lead.ProjectID != p.ID {
				t.Fatalf("lead project/harness/model = %q/%q/%q, want %q/%q/%q", lead.ProjectID, lead.Harness, lead.Model, p.ID, tc.want, tc.model)
			}
			proc := e.chat.Last(lead.ID)
			if proc == nil || proc.Spec.Harness != tc.want || proc.Spec.Model != tc.model || p.LeadID != lead.ID {
				t.Fatalf("lead process = %+v, project = %+v", proc, p)
			}
		})
	}
}

func TestProjectCreateRejectsUnknownHarnessBeforeSideEffects(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	folder := filepath.Join(e.workspace, "unknown-harness")
	if _, err := f.CreateProject(OwnerCaller, ProjectRequest{Name: "unknown-harness", Folder: folder, Harness: "not-a-harness"}); KindOf(err) != KindInvalid {
		t.Fatalf("unknown harness error = %v, want invalid", err)
	}
	if _, err := os.Stat(folder); !os.IsNotExist(err) {
		t.Fatalf("invalid request created folder: %v", err)
	}
	snap, err := f.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range snap.Projects {
		if p.Slug == "unknown-harness" {
			t.Fatalf("invalid request persisted project: %+v", p)
		}
	}
}

func TestLaunchPolicy(t *testing.T) {
	e := newUnguardedEnv(t)
	f := e.started()
	p, _ := f.CreateProject(OwnerCaller, ProjectRequest{Name: "alpha"})
	other, _ := f.CreateProject(OwnerCaller, ProjectRequest{Name: "beta"})
	lead := mustAgent(t, f, "alpha-lead")
	lc := callerFor(t, f, e.chat, lead.ID)

	w, err := f.LaunchAgent(lc, LaunchRequest{Name: "w1", Prompt: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	if w.Level != model.LevelWorker || w.Model != sonnet || w.ParentID != lead.ID || w.ProjectID != p.ID || w.Cwd != p.Folder {
		t.Fatalf("worker: %+v", w)
	}
	if s := e.chat.Last(w.ID).Sent(); len(s) != 1 || s[0] != "do it" {
		t.Fatalf("worker prompt: %q", s)
	}
	cases := []struct {
		name string
		c    Caller
		req  LaunchRequest
		kind Kind
	}{
		{"worker cannot launch", callerFor(t, f, e.chat, w.ID), LaunchRequest{Name: "w2"}, KindForbidden},
		{"wrong model", lc, LaunchRequest{Name: "w3", Model: "unsupported-model"}, KindForbidden},
		{"lead cannot launch lead", lc, LaunchRequest{Name: "l2", Level: model.LevelLead}, KindForbidden},
		{"lead stays in its project", lc, LaunchRequest{Name: "w4", ProjectID: other.ID}, KindForbidden},
		{"nobody launches a head", OwnerCaller, LaunchRequest{Name: "h2", Level: model.LevelHead}, KindForbidden},
		{"duplicate name", OwnerCaller, LaunchRequest{Name: "w1"}, KindConflict},
		{"bad name", OwnerCaller, LaunchRequest{Name: "Bad Name"}, KindInvalid},
		{"second lead", OwnerCaller, LaunchRequest{Name: "l3", Level: model.LevelLead, ProjectID: "alpha"}, KindConflict},
		{"owner wrong model", OwnerCaller, LaunchRequest{Name: "w5", Model: "gpt"}, KindForbidden},
	}
	for _, tc := range cases {
		if _, err := f.LaunchAgent(tc.c, tc.req); KindOf(err) != tc.kind {
			t.Errorf("%s: err %v (kind %d), want kind %d", tc.name, err, KindOf(err), tc.kind)
		}
	}
	// Owner may launch an allowed explicit model and a shell in terminal mode.
	if _, err := f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "w6", ProjectID: "alpha", Model: sonnet}); err != nil {
		t.Fatal(err)
	}
	sh, err := f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "sh", Harness: model.HarnessShell})
	if err != nil || sh.Mode != model.ModeTerminal || sh.Model != "" || e.term.Last(sh.ID) == nil {
		t.Fatalf("shell: %+v %v", sh, err)
	}
	if w6 := mustAgent(t, f, "w6"); w6.ParentID != lead.ID {
		t.Fatalf("owner-launched worker parent = %s, want project lead", w6.ParentID)
	}
}

func TestActPermissions(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	f.CreateProject(OwnerCaller, ProjectRequest{Name: "alpha"})
	lead := mustAgent(t, f, "alpha-lead")
	lc := callerFor(t, f, e.chat, lead.ID)
	w, _ := f.LaunchAgent(lc, LaunchRequest{Name: "w1"})
	wc := callerFor(t, f, e.chat, w.ID)

	if _, err := f.StopAgent(lc, "alpha-lead"); KindOf(err) != KindForbidden {
		t.Fatalf("self stop: %v", err)
	}
	if _, err := f.StopAgent(lc, "head"); KindOf(err) != KindForbidden {
		t.Fatalf("lead stops head: %v", err)
	}
	if _, err := f.StopAgent(wc, "alpha-lead"); KindOf(err) != KindForbidden {
		t.Fatalf("worker stops lead: %v", err)
	}
	a, err := f.StopAgent(lc, "w1")
	if err != nil || a.State != model.StateStopped || a.Desired != "stopped" {
		t.Fatalf("lead stops worker: %+v %v", a, err)
	}
	if !e.chat.Procs()[len(e.chat.Procs())-1].Stopped() {
		t.Fatal("process not stopped")
	}
	if _, ok := f.CallerForToken("x"); ok {
		t.Fatal("bogus token accepted")
	}
	a, err = f.StartAgent(lc, "w1")
	if err != nil || a.State != model.StateStarting || a.Desired != "running" {
		t.Fatalf("restart: %+v %v", a, err)
	}
	if err := f.RemoveAgent(lc, "w1"); KindOf(err) != KindForbidden {
		t.Fatal("lead removed an agent")
	}
	if err := f.RemoveAgent(OwnerCaller, "head"); KindOf(err) != KindForbidden {
		t.Fatal("head removed")
	}
}

func TestApprovalFlow(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	head := mustAgent(t, f, "head")
	proc := e.chat.Last(head.ID)
	sub := f.Subscribe()
	defer f.Unsubscribe(sub)

	proc.State(model.StateWorking)
	proc.Emit(runner.Event{Kind: runner.EvApproval, Approval: &runner.ApprovalRequest{
		ID: "req-1", Tool: "Bash", Input: json.RawMessage(`{"command":"rm -rf build"}`), Description: "delete build"}})
	waitState(t, f, "head", model.StateWaiting)
	pend, _ := f.Approvals(model.ApprovalPending)
	if len(pend) != 1 || pend[0].Tool != "Bash" || pend[0].Kind != model.ApprovalPermission {
		t.Fatalf("pending: %+v", pend)
	}
	// While waiting, a "working" from the runner does not hide the open request.
	proc.State(model.StateWorking)
	time.Sleep(20 * time.Millisecond)
	if mustAgent(t, f, "head").State != model.StateWaiting {
		t.Fatal("waiting lost")
	}
	if _, err := f.Decide(callerFor(t, f, e.chat, head.ID), pend[0].ID, DecideRequest{Decision: "allow"}); KindOf(err) != KindForbidden {
		t.Fatal("agent decided its own approval")
	}
	ap, err := f.Decide(OwnerCaller, pend[0].ID, DecideRequest{Decision: "allow"})
	if err != nil || ap.State != model.ApprovalAllowed {
		t.Fatalf("decide: %+v %v", ap, err)
	}
	if ans := proc.Answers(); len(ans) != 1 || ans[0].RequestID != "req-1" || !ans[0].Decision.Allow {
		t.Fatalf("answers: %+v", ans)
	}
	if st := mustAgent(t, f, "head").State; st != model.StateWorking {
		t.Fatalf("state after allow: %s", st)
	}
	if _, err := f.Decide(OwnerCaller, pend[0].ID, DecideRequest{Decision: "deny"}); KindOf(err) != KindConflict {
		t.Fatal("decided twice")
	}

	// Deny with a message.
	proc.Emit(runner.Event{Kind: runner.EvApproval, Approval: &runner.ApprovalRequest{ID: "req-2", Tool: "Write"}})
	waitState(t, f, "head", model.StateWaiting)
	pend, _ = f.Approvals(model.ApprovalPending)
	f.Decide(OwnerCaller, pend[0].ID, DecideRequest{Decision: "deny", Message: "not now"})
	if ans := proc.Answers(); len(ans) != 2 || ans[1].Decision.Allow || ans[1].Decision.Message != "not now" {
		t.Fatalf("deny: %+v", ans)
	}

	// Withdrawn by the harness → expired.
	proc.Emit(runner.Event{Kind: runner.EvApproval, Approval: &runner.ApprovalRequest{ID: "req-3", Tool: "Edit"}})
	waitState(t, f, "head", model.StateWaiting)
	proc.Emit(runner.Event{Kind: runner.EvApprovalCancelled, Approval: &runner.ApprovalRequest{ID: "req-3"}})
	waitState(t, f, "head", model.StateWorking)
	if pend, _ = f.Approvals(model.ApprovalPending); len(pend) != 0 {
		t.Fatalf("cancelled still pending: %+v", pend)
	}

	// Process death expires open requests.
	proc.Emit(runner.Event{Kind: runner.EvApproval, Approval: &runner.ApprovalRequest{ID: "req-4", Tool: "Edit"}})
	waitState(t, f, "head", model.StateWaiting)
	pend, _ = f.Approvals(model.ApprovalPending)
	proc.Exit(1, nil)
	waitState(t, f, "head", model.StateError)
	got, _ := e.st.GetApproval(pend[0].ID)
	if got.State != model.ApprovalExpired {
		t.Fatalf("after exit: %s", got.State)
	}

	types := map[string]int{}
	for len(sub.C()) > 0 {
		types[(<-sub.C()).Type]++
	}
	if types[model.EvApprovalCreated] != 4 || types[model.EvApprovalResolved] != 4 {
		t.Fatalf("bus events: %v", types)
	}
}

func TestQuestionAnsweredAsMail(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	head := mustAgent(t, f, "head")
	proc := e.chat.Last(head.ID)
	hc := callerFor(t, f, e.chat, head.ID)
	q, err := f.AskOwner(hc, AskRequest{Question: "Which DB?", Options: []string{"sqlite", "postgres"}})
	if err != nil || q.Kind != model.ApprovalQuestion || len(q.Options) != 2 {
		t.Fatalf("ask: %+v %v", q, err)
	}
	if _, err := f.Decide(OwnerCaller, q.ID, DecideRequest{Decision: "allow", Version: q.Version}); KindOf(err) != KindInvalid {
		t.Fatalf("empty answer accepted: %v", err)
	}
	if _, err := f.Decide(OwnerCaller, q.ID, DecideRequest{Decision: "answer", Answer: "sqlite", Version: q.Version}); err != nil {
		t.Fatal(err)
	}
	sent := proc.Sent()
	if len(sent) != 1 || !strings.Contains(sent[0], "Which DB?") || !strings.Contains(sent[0], "sqlite") {
		t.Fatalf("answer mail: %q", sent)
	}
}

func TestMailBatchingAtIdle(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	f.CreateProject(OwnerCaller, ProjectRequest{Name: "alpha"})
	lead := mustAgent(t, f, "alpha-lead")
	lp := e.chat.Last(lead.ID)
	lp.State(model.StateWorking) // busy with its kickoff
	lc := callerFor(t, f, e.chat, lead.ID)
	w, _ := f.LaunchAgent(lc, LaunchRequest{Name: "w1"})
	wc := callerFor(t, f, e.chat, w.ID)

	for _, body := range []string{"step 1 done", "step 2 done"} {
		if _, err := f.SendMessage(wc, SendRequest{To: "alpha-lead", Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(lp.Sent()); n != 1 { // only the kickoff
		t.Fatalf("mail delivered while working: %d sends", n)
	}
	if q := mustAgent(t, f, "alpha-lead").Queued; q != 2 {
		t.Fatalf("queued = %d", q)
	}
	lp.Turn("planned", 0.25, 1)
	eventually(t, "batched delivery", func() bool { return len(lp.Sent()) == 2 })
	batch := lp.Sent()[1]
	if batch != "[message from w1 (worker)] step 1 done\n\n[message from w1 (worker)] step 2 done" {
		t.Fatalf("batch: %q", batch)
	}
	a := mustAgent(t, f, "alpha-lead")
	if a.Queued != 0 || a.State != model.StateWorking || a.CostUSD != 0.25 || a.Turns != 1 {
		t.Fatalf("lead after delivery: %+v", a)
	}
	entries, _ := f.Timeline("alpha-lead", 0, 0)
	in := 0
	for _, en := range entries {
		if en.Kind == model.EntryMessageIn && en.Meta["from"] == w.ID {
			in++
		}
	}
	if in != 2 {
		t.Fatalf("message_in entries: %d", in)
	}
	// Reading the inbox marks the mail read.
	msgs, _ := f.Messages(lc, "", 0)
	for _, m := range msgs {
		if m.To == lead.ID && m.State != model.MsgRead {
			t.Fatalf("not read: %+v", m)
		}
	}
	// Usage adds per result.
	lp.Turn("again", 0.5, 2)
	eventually(t, "cost", func() bool { return mustAgent(t, f, "alpha-lead").CostUSD == 0.75 })
	// Reply goes back to the sender.
	m, err := f.SendMessage(lc, SendRequest{ReplyTo: msgs[0].ID, Body: "thanks"})
	if err != nil || m.To != w.ID {
		t.Fatalf("reply: %+v %v", m, err)
	}
	// Mail to the owner is delivered at once.
	om, err := f.SendMessage(lc, SendRequest{To: "owner", Body: "status"})
	if err != nil || om.State != model.MsgDelivered {
		t.Fatalf("to owner: %+v %v", om, err)
	}
}

func TestOwnerMailImmediateAndQueuedWhenStopped(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	head := mustAgent(t, f, "head")
	hp := e.chat.Last(head.ID)
	hp.State(model.StateWorking)
	waitState(t, f, "head", model.StateWorking)
	m, err := f.SendOwner("head", "hello", "")
	if err != nil || m.State != model.MsgDelivered {
		t.Fatalf("%+v %v", m, err)
	}
	if s := hp.Sent(); len(s) != 1 || s[0] != "hello" {
		t.Fatalf("owner text: %q", s)
	}
	f.StopAgent(OwnerCaller, "head")
	m2, _ := f.SendOwner("head", "while stopped", "")
	if m2.State != model.MsgQueued {
		t.Fatalf("stopped: %+v", m2)
	}
	f.StartAgent(OwnerCaller, "head")
	np := e.chat.Last(head.ID)
	if np == hp {
		t.Fatal("not restarted")
	}
	np.State(model.StateIdle) // ready (claude answered initialize)
	eventually(t, "queued owner mail on start", func() bool {
		s := np.Sent()
		return len(s) == 1 && s[0] == "[message from owner] while stopped"
	})
}

func TestMailLimits(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	f.CreateProject(OwnerCaller, ProjectRequest{Name: "alpha"})
	lead := mustAgent(t, f, "alpha-lead")
	e.chat.Last(lead.ID).State(model.StateWorking)
	waitState(t, f, "alpha-lead", model.StateWorking)
	hc := callerFor(t, f, e.chat, mustAgent(t, f, "head").ID)
	if _, err := f.SendMessage(hc, SendRequest{To: "alpha-lead", Body: strings.Repeat("x", MaxBodyBytes+1)}); KindOf(err) != KindInvalid {
		t.Fatalf("long body: %v", err)
	}
	if _, err := f.SendMessage(hc, SendRequest{To: "head", Body: "me"}); KindOf(err) != KindInvalid {
		t.Fatalf("to self: %v", err)
	}
	for i := 0; i < MaxPerWindow; i++ {
		if _, err := f.SendMessage(hc, SendRequest{To: "alpha-lead", Body: "x"}); err != nil {
			t.Fatalf("msg %d: %v", i, err)
		}
	}
	if _, err := f.SendMessage(hc, SendRequest{To: "alpha-lead", Body: "x"}); KindOf(err) != KindTooMany {
		t.Fatalf("rate limit: %v", err)
	}
}

func TestRestartRecovery(t *testing.T) {
	e := newEnv(t)
	f1 := e.started()
	f1.CreateProject(OwnerCaller, ProjectRequest{Name: "alpha"})
	lead := mustAgent(t, f1, "alpha-lead")
	lp := e.chat.Last(lead.ID)
	lp.Emit(runner.Event{Kind: runner.EvSession, SessionID: "11111111-2222-4333-8444-555555555555"})
	lp.Turn("ok", 0.1, 1)
	lp.Emit(runner.Event{Kind: runner.EvApproval, Approval: &runner.ApprovalRequest{ID: "r1", Tool: "Bash"}})
	waitState(t, f1, "alpha-lead", model.StateWaiting)
	w, _ := f1.LaunchAgent(OwnerCaller, LaunchRequest{Name: "w1", ProjectID: "alpha"})
	f1.StopAgent(OwnerCaller, "w1")
	q, _ := f1.AskOwner(callerFor(t, f1, e.chat, lead.ID), AskRequest{Question: "?"})
	before := len(e.chat.Procs())

	// The daemon "crashes": a new fleet starts on the same database.
	f2 := e.started()
	pend, _ := f2.Approvals(model.ApprovalPending)
	if len(pend) != 1 || pend[0].ID != q.ID {
		t.Fatalf("pending after restart (want only the question): %+v", pend)
	}
	procs := e.chat.Procs()[before:]
	started := map[string]runner.Spec{}
	for _, p := range procs {
		started[p.Spec.AgentID] = p.Spec
	}
	if _, ok := started[w.ID]; ok {
		t.Fatal("stopped worker was restarted")
	}
	ls, ok := started[lead.ID]
	if !ok || ls.SessionID != "11111111-2222-4333-8444-555555555555" || len(ls.ExtraArgs) != 0 {
		t.Fatalf("lead not resumed: %+v", ls)
	}
	hs, ok := started[mustAgent(t, f2, "head").ID]
	if !ok || hs.SessionID != "" || hs.ExtraArgs[0] != "--session-id" {
		t.Fatalf("head (never talked) should reuse its pre-assigned id as new: %+v", hs)
	}
	if a := mustAgent(t, f2, "alpha-lead"); a.State != model.StateStarting || a.CostUSD != 0.1 {
		t.Fatalf("lead after restart: %+v", a)
	}
	if a := mustAgent(t, f2, "w1"); a.State != model.StateStopped {
		t.Fatalf("worker after restart: %+v", a)
	}
}

func TestDeleteProjectKeepsFolder(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	p, _ := f.CreateProject(OwnerCaller, ProjectRequest{Name: "alpha"})
	f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "w1", ProjectID: p.ID})
	if err := f.DeleteProject(callerFor(t, f, e.chat, mustAgent(t, f, "head").ID), p.ID); KindOf(err) != KindForbidden {
		t.Fatal("head deleted a project")
	}
	if err := f.DeleteProject(OwnerCaller, "alpha"); err != nil {
		t.Fatal(err)
	}
	snap, _ := f.Snapshot()
	if len(snap.Projects) != 0 || len(snap.Agents) != 1 {
		t.Fatalf("after delete: %d projects, %d agents", len(snap.Projects), len(snap.Agents))
	}
	if _, err := os.Stat(p.Folder); err != nil {
		t.Fatal("folder deleted")
	}
}

func TestRemoveAgentRemovesSubtree(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	f.CreateProject(OwnerCaller, ProjectRequest{Name: "alpha"})
	f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "w1", ProjectID: "alpha"})
	if err := f.RemoveAgent(OwnerCaller, "alpha-lead"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Agent("w1"); KindOf(err) != KindNotFound {
		t.Fatal("worker survived its lead")
	}
	p, _ := f.Project("alpha")
	if p.LeadID != "" {
		t.Fatal("lead_id not cleared")
	}
}

func TestStartFailureSetsError(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	e.chat.FailStarts(os.ErrPermission)
	a, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "w1"})
	if err != nil || a.State != model.StateError || !strings.Contains(a.Error, "start failed") {
		t.Fatalf("%+v %v", a, err)
	}
}

func TestTimelineIgnoresRunnerUserEcho(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	head := mustAgent(t, f, "head")
	hp := e.chat.Last(head.ID)
	f.SendOwner("head", "hi", "")
	hp.Entry(model.EntryUser, "hi") // the claude runner echoes sent text
	hp.Turn("hello owner", 0, 1)
	eventually(t, "turn", func() bool { return mustAgent(t, f, "head").State == model.StateIdle })
	entries, _ := f.Timeline("head", 0, 0)
	var kinds []string
	for i, en := range entries {
		kinds = append(kinds, string(en.Kind))
		if en.Seq != int64(i+1) {
			t.Fatalf("seq gap: %+v", entries)
		}
	}
	if strings.Join(kinds, ",") != "user,assistant,result" {
		t.Fatalf("kinds: %v", kinds)
	}
}

func TestBusDropsAndFlagsResync(t *testing.T) {
	b := newBus()
	s := b.Subscribe()
	for i := 0; i < subBuffer+10; i++ {
		b.Publish(model.BusEvent{Type: "x"})
	}
	if len(s.C()) != subBuffer || !s.TakeResync() || s.TakeResync() {
		t.Fatal("resync flag")
	}
	b.Unsubscribe(s)
	b.Publish(model.BusEvent{Type: "y"}) // no panic, no block
}

func TestTerminalFanOut(t *testing.T) {
	e := newUnguardedEnv(t)
	f := e.started()
	sh, _ := f.LaunchAgent(OwnerCaller, LaunchRequest{Name: "sh", Harness: model.HarnessShell})
	p := e.term.Last(sh.ID)
	p.SetReplay([]byte("$ "))
	sub, err := f.AttachTerminal("sh")
	if err != nil || string(sub.Replay) != "$ " {
		t.Fatalf("attach: %v %q", err, sub.Replay)
	}
	p.Emit(runner.Event{Kind: runner.EvOutput, Output: []byte("ls\n")})
	select {
	case b := <-sub.C():
		if string(b) != "ls\n" {
			t.Fatalf("%q", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no output")
	}
	if err := f.TerminalInput(AgentCaller("x"), "sh", []byte("x")); KindOf(err) != KindForbidden {
		t.Fatal("agent typed")
	}
	f.TerminalInput(OwnerCaller, "sh", []byte("pwd\r"))
	f.TerminalResize(OwnerCaller, "sh", 100, 30)
	if string(p.Written()) != "pwd\r" {
		t.Fatal("input not written")
	}
	if c, r := p.Size(); c != 100 || r != 30 {
		t.Fatal("not resized")
	}
	p.Exit(0, nil)
	eventually(t, "sub closed", func() bool {
		select {
		case _, ok := <-sub.C():
			return !ok
		default:
			return false
		}
	})
	f.DetachTerminal(sub)
}

func TestSetPolicy(t *testing.T) {
	e := newEnv(t)
	f := e.started()
	pol := model.DefaultPolicy()
	lp := pol.Levels[model.LevelWorker]
	lp.AllowedModels = append(lp.AllowedModels, opus)
	pol.Levels[model.LevelWorker] = lp
	if _, err := f.SetPolicy(AgentCaller("x"), pol); KindOf(err) != KindForbidden {
		t.Fatal("agent set policy")
	}
	if _, err := f.SetPolicy(OwnerCaller, pol); err != nil {
		t.Fatal(err)
	}
	if _, err := f.LaunchAgent(OwnerCaller, LaunchRequest{ProjectID: workerProject(t, f), Name: "w1", Model: opus}); err != nil {
		t.Fatalf("opus worker after policy change: %v", err)
	}
	bad := model.DefaultPolicy()
	delete(bad.Levels, model.LevelLead)
	if _, err := f.SetPolicy(OwnerCaller, bad); KindOf(err) != KindInvalid {
		t.Fatal("invalid policy accepted")
	}
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{"My App!": "my-app", "  --x--  ": "x", "Ünïcode Ok": "n-code-ok", "": ""} {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Slugify(strings.Repeat("a", 60)); len(got) != maxSlug {
		t.Errorf("long slug %d", len(got))
	}
}
