package claude

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/runner"
)

type watcher struct {
	t    *testing.T
	proc runner.Process
	seen []string
}

func startFake(t *testing.T, spec runner.Spec) *watcher {
	t.Helper()
	spec.Command = os.Args[0]
	spec.Env = append([]string{EnvConfigDir + "=" + t.TempDir()}, append(spec.Env, "TMXG_FAKE_CLAUDE=1")...)
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
	w := &watcher{t: t, proc: p}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		go func() { // drain whatever the test did not read
			for range p.Events() {
			}
		}()
		_ = p.Stop(ctx)
	})
	return w
}

// until reads events until one is described as want (prefix match) and returns it.
func (w *watcher) until(want string) runner.Event {
	w.t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-w.proc.Events():
			if !ok {
				w.t.Fatalf("events closed before %q; seen %q", want, w.seen)
			}
			d := describe(ev)
			if ev.Kind == runner.EvExit {
				d = "exit"
			}
			w.seen = append(w.seen, d)
			if strings.HasPrefix(d, want) {
				return ev
			}
		case <-timeout:
			w.t.Fatalf("timed out waiting for %q; seen %q", want, w.seen)
		}
	}
}

func TestProcessTurnWithApproval(t *testing.T) {
	w := startFake(t, runner.Spec{Name: "w1", Model: "claude-sonnet-5-5"})
	w.until("state:starting")
	w.until("state:idle") // initialize answered

	if err := w.proc.Send("run echo hi"); err != nil {
		t.Fatal(err)
	}
	w.until("timeline:user:run echo hi")
	w.until("state:working")
	if ev := w.until("session:"); ev.SessionID != "fake-session" {
		t.Fatalf("session %q", ev.SessionID)
	}
	w.until("timeline:tool_use:Bash")
	ap := w.until("approval:perm-1:Bash").Approval
	w.until("state:waiting")

	if err := w.proc.Answer("nope", runner.Decision{Allow: true}); err == nil {
		t.Fatal("answering an unknown request should fail")
	}
	upd := json.RawMessage(`{"command":"echo changed"}`)
	if err := w.proc.Answer(ap.ID, runner.Decision{Allow: true, UpdatedInput: upd}); err != nil {
		t.Fatal(err)
	}
	w.until("state:working")
	w.until("timeline:tool_result:ran: echo changed")
	w.until("timeline:assistant:done")
	w.until("timeline:result:done")
	if ev := w.until("usage:"); ev.Turns != 1 || ev.CostUSD != 0.01 {
		t.Fatalf("usage %+v", ev)
	}
	w.until("state:idle")

	// second turn, denied this time
	if err := w.proc.Send("run rm -rf /"); err != nil {
		t.Fatal(err)
	}
	ap = w.until("approval:").Approval
	if string(ap.Input) != `{"command":"rm -rf /"}` {
		t.Fatalf("input %s", ap.Input)
	}
	if err := w.proc.Answer(ap.ID, runner.Decision{Message: "not that"}); err != nil {
		t.Fatal(err)
	}
	if ev := w.until("timeline:tool_result:"); !ev.Entry.IsError || ev.Entry.Text != "denied: not that" {
		t.Fatalf("tool_result %+v", ev.Entry)
	}
	w.until("state:idle")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = w.proc.Stop(ctx) }()
	if ev := w.until("exit"); ev.ExitCode != 0 || ev.Err != nil {
		t.Fatalf("exit %+v", ev)
	}
	if _, ok := <-w.proc.Events(); ok {
		t.Fatal("events not closed after exit")
	}
}

func TestProcessArgsAndResume(t *testing.T) {
	w := startFake(t, runner.Spec{Model: "m1", SessionID: "old-sid", AllowedTools: []string{"mcp__tmxg"}})
	w.until("state:idle")
	if err := w.proc.Send("args"); err != nil {
		t.Fatal(err)
	}
	ev := w.until("timeline:result:")
	var args []string
	if err := json.Unmarshal([]byte(ev.Entry.Text), &args); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"-p", "--permission-prompt-tool stdio", "--model m1", "--resume old-sid", "--allowedTools=mcp__tmxg"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv %q lacks %q", joined, want)
		}
	}
	// the resumed id is already known: no EvSession for it
	for _, s := range w.seen {
		if strings.HasPrefix(s, "session:") {
			t.Errorf("unexpected %s", s)
		}
	}
}

func TestProcessInterrupt(t *testing.T) {
	w := startFake(t, runner.Spec{})
	w.until("state:idle")
	if err := w.proc.Send("wait"); err != nil {
		t.Fatal(err)
	}
	w.until("state:working")
	if err := w.proc.Interrupt(); err != nil {
		t.Fatal(err)
	}
	if ev := w.until("timeline:result:"); !ev.Entry.IsError {
		t.Fatalf("result %+v", ev.Entry)
	}
	w.until("state:idle")
}

func TestProcessCrashReportsStderr(t *testing.T) {
	w := startFake(t, runner.Spec{})
	w.until("state:idle")
	_ = w.proc.Send("crash")
	ev := w.until("exit")
	if ev.ExitCode != 3 || ev.Err == nil || !strings.Contains(ev.Err.Error(), "boom: something broke") {
		t.Fatalf("exit %+v", ev)
	}
}

func TestProcessStopKillsHungProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no SIGTERM on Windows: terminate already kills the tree")
	}
	w := startFake(t, runner.Spec{})
	w.until("state:idle")
	_ = w.proc.Send("hang")
	w.until("timeline:assistant:hanging")

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	errc := make(chan error, 1)
	go func() { errc <- w.proc.Stop(ctx) }()
	ev := w.until("exit")
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	// SIGTERM is ignored, so the kill comes killGrace after ctx expired
	if el := time.Since(start); el < killGrace {
		t.Fatalf("stopped after %v, before the kill grace", el)
	}
	if ev.ExitCode == 0 {
		t.Fatalf("exit %+v", ev)
	}
	if w.proc.PID() <= 0 {
		t.Fatal("no pid")
	}
}
