package term

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/harness"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/runner/claude"
)

// tuiEvents records a harness process's events.
type tuiEvents struct {
	t       *testing.T
	p       runner.Process
	ch      chan runner.Event
	out     strings.Builder
	states  []model.AgentState
	session string
}

func watchTUI(t *testing.T, p runner.Process) *tuiEvents {
	e := &tuiEvents{t: t, p: p, ch: make(chan runner.Event, 4096)}
	go func() {
		for ev := range p.Events() {
			e.ch <- ev
		}
		close(e.ch)
	}()
	return e
}

// until consumes events until ok returns true.
func (e *tuiEvents) until(what string, ok func(runner.Event) bool) runner.Event {
	e.t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case ev, open := <-e.ch:
			if !open {
				e.t.Fatalf("events ended waiting for %s; output %q", what, e.out.String())
			}
			switch ev.Kind {
			case runner.EvOutput:
				e.out.Write(ev.Output)
			case runner.EvState:
				e.states = append(e.states, ev.State)
			case runner.EvSession:
				e.session = ev.SessionID
			}
			if ok(ev) {
				return ev
			}
		case <-deadline:
			e.t.Fatalf("no %s; states %v", what, e.states)
		}
	}
}

func (e *tuiEvents) state(s model.AgentState) {
	e.t.Helper()
	e.until("state "+string(s), func(ev runner.Event) bool { return ev.Kind == runner.EvState && ev.State == s })
}

func startFakeTUI(t *testing.T, extraEnv []string, extraArgs ...string) (runner.Process, *tuiEvents) {
	t.Helper()
	return startFakeHarness(t, runner.Spec{Harness: model.HarnessClaude, Env: extraEnv, ExtraArgs: extraArgs})
}

// startFakeHarness runs the fake TUI as spec's harness (its flavor of screens), with the
// flags and env tmx gives that harness.
func startFakeHarness(t *testing.T, spec runner.Spec) (runner.Process, *tuiEvents) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake TUI uses stty")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	spec.Command, spec.Cwd, spec.Cols, spec.Rows = exe, t.TempDir(), 100, 30
	spec.Env = append([]string{"TMXG_FAKE_TUI=1", "TMXG_FAKE_FLAVOR=" + string(spec.Harness),
		claude.EnvConfigDir + "=" + filepath.Join(home, "claude-config")}, spec.Env...) // an isolated claude, as the fleet starts it
	p, err := New().Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = p.Stop(ctx)
	})
	return p, watchTUI(t, p)
}

func TestHarnessTUIStatesMessagesAndMenus(t *testing.T) {
	p, ev := startFakeTUI(t, nil, "--session-id", "sess-1")
	// A message sent before the input box is ready waits for it (the initial prompt).
	if err := p.Send("first"); err != nil {
		t.Fatal(err)
	}
	ev.until("the queued message", func(e runner.Event) bool { return strings.Contains(ev.out.String(), "got: first") })
	ev.state(model.StateIdle)
	if ev.session == "" {
		ev.until("the session", func(e runner.Event) bool { return e.Kind == runner.EvSession })
	}
	if ev.session != "sess-1" {
		t.Fatalf("session %q", ev.session)
	}

	if err := p.Send("hello\nworld"); err != nil {
		t.Fatal(err)
	}
	ev.state(model.StateWorking)
	ev.until("the reply", func(e runner.Event) bool {
		return strings.Contains(ev.out.String(), "got: hello\r\nworld") || strings.Contains(ev.out.String(), "got: hello")
	})
	ev.state(model.StateIdle)

	// A permission menu becomes an approval; denying picks "No".
	_ = p.Send("perm")
	ap := ev.until("an approval", func(e runner.Event) bool { return e.Kind == runner.EvApproval }).Approval
	if ap.Tool != model.ToolTerminalMenu || ap.Kind != model.ApprovalPermission || len(ap.Options) != 2 ||
		ap.Options[0] != "Yes" || !strings.Contains(ap.Question, "Do you want to proceed?") {
		t.Fatalf("%+v", ap)
	}
	ev.state(model.StateWaiting)
	if err := p.Answer("nope", runner.Decision{Allow: true}); err == nil {
		t.Fatal("answered an unknown menu")
	}
	if err := p.Answer(ap.ID, runner.Decision{Allow: false}); err != nil {
		t.Fatal(err)
	}
	ev.until("the choice", func(e runner.Event) bool {
		if e.Kind == runner.EvApprovalCancelled {
			t.Fatal("an answered menu was withdrawn")
		}
		return strings.Contains(ev.out.String(), "chose: No, and tell Claude")
	})
	ev.state(model.StateIdle)

	// A menu answered by a human in the terminal is withdrawn.
	_ = p.Send("perm")
	ap = ev.until("a second approval", func(e runner.Event) bool { return e.Kind == runner.EvApproval }).Approval
	_ = p.Write([]byte("\x1b[5;1R\r"), true) // a viewer's cursor report is dropped; Enter picks "Yes"
	ev.until("the withdrawal", func(e runner.Event) bool { return e.Kind == runner.EvApprovalCancelled && e.Approval.ID == ap.ID })
	if !strings.Contains(ev.out.String(), "chose: Yes") {
		t.Fatalf("output %q", ev.out.String())
	}
}

func TestHarnessTUITrustGateIsAnswered(t *testing.T) {
	p, ev := startFakeTUI(t, []string{"TMXG_FAKE_TRUST=1"})
	note := ev.until("the gate note", func(e runner.Event) bool {
		if e.Kind == runner.EvApproval {
			t.Fatalf("the trust gate became an approval: %+v", e.Approval)
		}
		return e.Kind == runner.EvTimeline && e.Entry.Kind == model.EntrySystem
	})
	if !strings.Contains(note.Entry.Text, "Yes, I trust this folder") {
		t.Fatal(note.Entry.Text)
	}
	ev.state(model.StateIdle)
	if !strings.Contains(ev.out.String(), "trusted") {
		t.Fatalf("%q", ev.out.String())
	}
	_ = p
}

// A menu answer moves the cursor one step at a time and presses Enter only on the right
// option, even when the dialog drops the first keys and redraws its cursor late.
func TestMenuAnswerStepsAndVerifiesTheCursor(t *testing.T) {
	p, ev := startFakeTUI(t, nil)
	ev.state(model.StateIdle)
	_ = p.Send("pick3")
	ap := ev.until("the dialog", func(e runner.Event) bool { return e.Kind == runner.EvApproval }).Approval
	if ap.Kind != model.ApprovalQuestion || strings.Join(ap.Options, ",") != "Option A,Option B,Option C" {
		t.Fatalf("%+v", ap)
	}
	if err := p.Answer(ap.ID, runner.Decision{Allow: true, Answer: "Option C"}); err != nil {
		t.Fatal(err)
	}
	ev.until("the choice", func(e runner.Event) bool { return strings.Contains(ev.out.String(), "chose: Option") })
	if out := ev.out.String(); !strings.Contains(out, "chose: Option C") {
		t.Fatalf("picked the wrong option: %q", out[max(0, len(out)-200):])
	}
}

func TestOpenCodeTUI(t *testing.T) {
	p, ev := startFakeHarness(t, runner.Spec{Harness: model.HarnessOpenCode, PermissionMode: "auto"})
	ev.state(model.StateIdle)
	_ = p.Send("hello")
	ev.state(model.StateWorking)
	ev.until("the reply", func(e runner.Event) bool { return strings.Contains(ev.out.String(), "got: hello") })
	ev.state(model.StateIdle)
	for _, c := range []struct {
		d    runner.Decision
		want string
	}{{runner.Decision{Allow: false}, "chose: Reject"}, {runner.Decision{Allow: true}, "chose: Allow once"}} {
		_ = p.Send("perm")
		ap := ev.until("an approval", func(e runner.Event) bool { return e.Kind == runner.EvApproval }).Approval
		if ap.Kind != model.ApprovalPermission || strings.Join(ap.Options, ",") != "Allow once,Allow always,Reject" {
			t.Fatalf("%+v", ap)
		}
		if err := p.Answer(ap.ID, c.d); err != nil {
			t.Fatal(err)
		}
		ev.until(c.want, func(e runner.Event) bool { return strings.Contains(ev.out.String(), c.want) })
		ev.state(model.StateIdle)
	}
}

func TestPiTUI(t *testing.T) {
	p, ev := startFakeHarness(t, runner.Spec{Harness: model.HarnessPi, SessionID: "0123abcd"})
	ev.state(model.StateIdle)
	if ev.session == "" {
		ev.until("the session", func(e runner.Event) bool { return e.Kind == runner.EvSession })
	}
	if ev.session != "0123abcd" {
		t.Fatalf("session %q", ev.session)
	}
	_ = p.Send("hello")
	ev.state(model.StateWorking)
	ev.until("the reply", func(e runner.Event) bool { return strings.Contains(ev.out.String(), "got: hello") })
	ev.state(model.StateIdle)
}

func TestStripQueryReplies(t *testing.T) {
	in := "a\x1b[12;40Rb\x1b[?62;22cc\x1b]11;rgb:ffff/ffff/ffff\x07d\x1b[A"
	if got := string(stripQueryReplies([]byte(in))); got != "abcd\x1b[A" {
		t.Fatalf("%q", got)
	}
}

func TestHarnessCommands(t *testing.T) {
	spec := runner.Spec{Harness: model.HarnessCodex, Command: "/x/codex", Model: "gpt-6", SystemPrompt: "be good",
		PermissionMode: "auto", Env: []string{"TMXG_AGENT_TOKEN=secret"},
		MCP: &runner.MCPServer{Command: "/x/tmx", Args: []string{"mcp"}, EnvKeys: []string{"TMXG_AGENT_TOKEN"}}}
	c, err := harness.TerminalCommand(spec)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(c.Args, " ")
	for _, want := range []string{"--no-daemon", "-m gpt-6", "mcp_servers.tmxg.command=\"/x/tmx\"", `mcp_servers.tmxg.env_vars=["TMXG_AGENT_TOKEN"]`, "[tmx launch binding: " + c.LaunchID + "]"} {
		if !strings.Contains(args, want) {
			t.Errorf("codex args miss %q: %s", want, args)
		}
	}
	if strings.Contains(args, "secret") {
		t.Fatal("the token is on the command line")
	}
	spec.SessionID = "abc"
	if c, _ := harness.TerminalCommand(spec); c.Args[0] != "resume" || c.Args[1] != "abc" || c.LaunchID != "" {
		t.Fatal(c.Args)
	}
	pi, _ := harness.TerminalCommand(runner.Spec{Harness: model.HarnessPi, Command: "/x/pi", SessionID: "s1", Model: "m"})
	if strings.Join(pi.Args, " ") != "--approve --session-id s1 --model m" {
		t.Fatal(pi.Args)
	}
	oc, _ := harness.TerminalCommand(runner.Spec{Harness: model.HarnessOpenCode, Command: "/x/oc", SessionID: "ses_abc",
		Model: "opencode-go/gpt-6", PermissionMode: "bypassPermissions",
		Env: []string{"TMXG_URL=u"}, MCP: &runner.MCPServer{Command: "/x/tmx", Args: []string{"mcp"}, EnvKeys: []string{"TMXG_URL"}}})
	if strings.Join(oc.Args, " ") != "--standalone -s ses_abc" {
		t.Fatalf("opencode args: %q", oc.Args)
	}
	env := strings.Join(oc.Env, "\n")
	if !strings.Contains(env, `"permission":"allow"`) || !strings.Contains(env, `"model":"opencode-go/gpt-6"`) ||
		!strings.Contains(env, `"environment":{"TMXG_URL":"u"}`) {
		t.Fatal(env)
	}
	plan, _ := harness.TerminalCommand(runner.Spec{Harness: model.HarnessOpenCode, Command: "/x/oc", PermissionMode: "plan"})
	if env := strings.Join(plan.Env, "\n"); !strings.Contains(env, `"permission":{"bash":"deny","edit":"deny"}`) {
		t.Fatal(env)
	}
}
