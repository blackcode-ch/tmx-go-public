package codex

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
)

type rec struct {
	t  *testing.T
	ch chan runner.Event
	// seen so far
	entries  []model.TimelineEntry
	states   []model.AgentState
	sessions []string
}

func start(t *testing.T, spec runner.Spec) (runner.Process, *rec) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	spec.Harness, spec.Command = model.HarnessCodex, exe
	spec.Env = append(spec.Env, "TMXG_FAKE_CODEX=1")
	if spec.Cwd == "" {
		spec.Cwd = t.TempDir()
	}
	p, err := New().Start(context.Background(), spec)
	if runner.Elevated() {
		if err == nil || !strings.Contains(err.Error(), "elevated") {
			t.Fatalf("elevated fake launch must be refused, got %v", err)
		}
		t.Skip("protocol integration requires an unelevated runner; elevation refusal verified")
	}
	if err != nil {
		t.Fatal(err)
	}
	r := &rec{t: t, ch: make(chan runner.Event, 1024)}
	go func() {
		for ev := range p.Events() {
			r.ch <- ev
		}
		close(r.ch)
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = p.Stop(ctx)
	})
	return p, r
}

func (r *rec) until(what string, ok func(runner.Event) bool) runner.Event {
	r.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, open := <-r.ch:
			if !open {
				r.t.Fatalf("events ended waiting for %s (entries %+v)", what, r.entries)
			}
			switch ev.Kind {
			case runner.EvTimeline:
				r.entries = append(r.entries, *ev.Entry)
			case runner.EvState:
				r.states = append(r.states, ev.State)
			case runner.EvSession:
				r.sessions = append(r.sessions, ev.SessionID)
			}
			if ok(ev) {
				return ev
			}
		case <-deadline:
			r.t.Fatalf("no %s; states %v entries %+v", what, r.states, r.entries)
		}
	}
}

func (r *rec) state(s model.AgentState) {
	r.t.Helper()
	r.until("state "+string(s), func(ev runner.Event) bool { return ev.Kind == runner.EvState && ev.State == s })
}

func (r *rec) result() model.TimelineEntry {
	r.t.Helper()
	return *r.until("a result", func(ev runner.Event) bool { return ev.Kind == runner.EvTimeline && ev.Entry.Kind == model.EntryResult }).Entry
}

func TestTurnSessionAndArgs(t *testing.T) {
	p, r := start(t, runner.Spec{Model: "gpt-fake", SystemPrompt: "you are tmx's", PermissionMode: "auto",
		Env: []string{"TMXG_AGENT_TOKEN=tok"}, MCP: &runner.MCPServer{Command: "/x/tmx", Args: []string{"mcp"}, EnvKeys: []string{"TMXG_AGENT_TOKEN"}}})
	// sent before the conversation exists: queued, then delivered
	if err := p.Send("args"); err != nil {
		t.Fatal(err)
	}
	r.state(model.StateWorking)
	res := r.result()
	r.state(model.StateIdle)
	if len(r.sessions) != 1 || r.sessions[0] != "thr-new" {
		t.Fatalf("sessions %v", r.sessions)
	}
	var got struct {
		Args   []string
		Token  string
		Thread string
	}
	if err := json.Unmarshal([]byte(res.Text), &got); err != nil {
		t.Fatalf("%q: %v", res.Text, err)
	}
	args := strings.Join(got.Args, " ")
	if got.Args[0] != "app-server" || !strings.Contains(args, `mcp_servers.tmxg.env_vars=["TMXG_AGENT_TOKEN"]`) || strings.Contains(args, "tok") {
		t.Fatalf("args %s", args)
	}
	if got.Token != "tok" {
		t.Fatal("the token is not in codex's environment")
	}
	for _, want := range []string{`"model":"gpt-fake"`, `"developerInstructions":"you are tmx's"`, `"approvalPolicy":"on-request"`} {
		if !strings.Contains(got.Thread, want) {
			t.Errorf("thread/start params miss %s: %s", want, got.Thread)
		}
	}

	_ = p.Send("hello")
	res = r.result()
	if res.Text != "echo: hello" || res.Meta["total_tokens"] != int64(1234) || res.Meta["subtype"] != "completed" {
		t.Fatalf("%+v", res)
	}
	var kinds []model.EntryKind
	for _, e := range r.entries {
		kinds = append(kinds, e.Kind)
	}
	if !strings.Contains(fmtKinds(kinds), "thinking assistant result") {
		t.Fatalf("kinds %v", kinds)
	}
}

func fmtKinds(k []model.EntryKind) string {
	s := make([]string, len(k))
	for i, x := range k {
		s[i] = string(x)
	}
	return strings.Join(s, " ")
}

func TestApprovalAllowAndDeny(t *testing.T) {
	p, r := start(t, runner.Spec{})
	r.state(model.StateIdle)
	for _, allow := range []bool{true, false} {
		_ = p.Send("run ls -la")
		ap := r.until("an approval", func(ev runner.Event) bool { return ev.Kind == runner.EvApproval }).Approval
		if ap.Tool != "Bash" || !strings.Contains(string(ap.Input), "ls -la") || ap.Description != "needs a shell" {
			t.Fatalf("%+v", ap)
		}
		r.state(model.StateWaiting)
		if err := p.Answer(ap.ID, runner.Decision{Allow: allow, Message: deniedDefault}); err != nil {
			t.Fatal(err)
		}
		r.state(model.StateWorking)
		tr := r.until("the tool result", func(ev runner.Event) bool {
			return ev.Kind == runner.EvTimeline && ev.Entry.Kind == model.EntryToolResult
		}).Entry
		if allow && (tr.Text != "ran: ls -la" || tr.IsError) || !allow && !tr.IsError {
			t.Fatalf("allow=%v: %+v", allow, tr)
		}
		r.result()
		r.state(model.StateIdle)
	}
	if err := p.Answer("7", runner.Decision{Allow: true}); err == nil {
		t.Fatal("answered twice")
	}
}

func TestQuestionWithdrawalElicitationInterruptAndFailure(t *testing.T) {
	p, r := start(t, runner.Spec{})
	r.state(model.StateIdle)

	_ = p.Send("ask")
	ap := r.until("a question", func(ev runner.Event) bool { return ev.Kind == runner.EvApproval }).Approval
	if ap.Kind != model.ApprovalQuestion || !strings.Contains(ap.Question, "Which color?") || strings.Join(ap.Options, ",") != "Red,Blue" {
		t.Fatalf("%+v", ap)
	}
	_ = p.Answer(ap.ID, runner.Decision{Allow: true, Answer: "Blue"})
	if res := r.result(); !strings.Contains(res.Text, `"q1":{"answers":["Blue"]}`) {
		t.Fatal(res.Text)
	}

	_ = p.Send("withdraw")
	ap = r.until("an approval", func(ev runner.Event) bool { return ev.Kind == runner.EvApproval }).Approval
	c := r.until("the withdrawal", func(ev runner.Event) bool { return ev.Kind == runner.EvApprovalCancelled })
	if c.Approval.ID != ap.ID {
		t.Fatalf("withdrew %s, asked %s", c.Approval.ID, ap.ID)
	}
	r.result()

	_ = p.Send("elicit")
	if res := r.result(); res.Text != "elicitation decline" {
		t.Fatal(res.Text)
	}

	_ = p.Send("wait")
	r.until("turn started", func(ev runner.Event) bool { return ev.Kind == runner.EvState && ev.State == model.StateWorking })
	time.Sleep(100 * time.Millisecond) // the turn id arrives with turn/started
	if err := p.Interrupt(); err != nil {
		t.Fatal(err)
	}
	if res := r.result(); res.Meta["subtype"] != "interrupted" {
		t.Fatalf("%+v", res)
	}

	_ = p.Send("fail")
	errs := r.until("the error", func(ev runner.Event) bool { return ev.Kind == runner.EvTimeline && ev.Entry.Kind == model.EntryError }).Entry
	if errs.Text != "401 Unauthorized" {
		t.Fatalf("a retried error was shown: %+v", errs)
	}
	if res := r.result(); !res.IsError || res.Text != "401 Unauthorized" {
		t.Fatalf("%+v", res)
	}
}

func TestResumeAndFallback(t *testing.T) {
	_, r := start(t, runner.Spec{SessionID: "thr-old"})
	r.state(model.StateIdle)
	if len(r.sessions) != 1 || r.sessions[0] != "thr-old" {
		t.Fatalf("%v", r.sessions)
	}
	_, r2 := start(t, runner.Spec{SessionID: "missing"})
	r2.state(model.StateIdle)
	if len(r2.sessions) != 1 || r2.sessions[0] != "thr-new" {
		t.Fatalf("%v", r2.sessions)
	}
	if len(r2.entries) == 0 || !strings.Contains(r2.entries[0].Text, "could not resume conversation missing") {
		t.Fatalf("%+v", r2.entries)
	}
}

func TestCrashReportsStderr(t *testing.T) {
	p, r := start(t, runner.Spec{})
	r.state(model.StateIdle)
	_ = p.Send("crash")
	ex := r.until("the exit", func(ev runner.Event) bool { return ev.Kind == runner.EvExit })
	if ex.ExitCode != 3 || ex.Err == nil || !strings.Contains(ex.Err.Error(), "boom: codex broke") {
		t.Fatalf("%+v", ex)
	}
}

func TestStopEndsOnEOF(t *testing.T) {
	p, r := start(t, runner.Spec{})
	r.state(model.StateIdle)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	begin := time.Now()
	if err := p.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if time.Since(begin) > 4*time.Second { // a -race binary lingers ~1 s at exit
		t.Fatal("stop waited for the deadline")
	}
	if ex := r.until("the exit", func(ev runner.Event) bool { return ev.Kind == runner.EvExit }); ex.ExitCode != 0 {
		t.Fatalf("%+v", ex)
	}
}

func TestWrongHarness(t *testing.T) {
	if _, err := New().Start(context.Background(), runner.Spec{Harness: model.HarnessClaude}); err == nil {
		t.Fatal("want error")
	}
}
