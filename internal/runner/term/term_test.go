package term

import (
	"bytes"
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
)

// collect gathers output and states from p until stop is closed or the channel ends.
type collector struct {
	out    chan []byte
	states chan model.AgentState
	exit   chan runner.Event
}

func watch(p runner.Process) *collector {
	c := &collector{out: make(chan []byte, 4096), states: make(chan model.AgentState, 64), exit: make(chan runner.Event, 1)}
	go func() {
		for ev := range p.Events() {
			switch ev.Kind {
			case runner.EvOutput:
				c.out <- ev.Output
			case runner.EvState:
				c.states <- ev.State
			case runner.EvExit:
				c.exit <- ev
			}
		}
	}()
	return c
}

// expect reads output until it contains want.
func (c *collector) expect(t *testing.T, got *bytes.Buffer, want string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for !strings.Contains(got.String(), want) {
		select {
		case b := <-c.out:
			got.Write(b)
		case <-deadline:
			t.Fatalf("no %q in output %q", want, got.String())
		}
	}
}

func (c *collector) state(t *testing.T, want model.AgentState) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case s := <-c.states:
			if s == want {
				return
			}
		case <-deadline:
			t.Fatalf("state %s never came", want)
		}
	}
}

// The owner's own shell (HarnessShell, which only the owner may launch: fleet.LaunchAgent)
// is the owner, not an agent: it must keep their usual gh/ssh/account environment. Every
// other harness still gets the owner-credential denylist (agentpolicy.FilterEnv).
func TestShellEnvIsNotFiltered(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	t.Setenv("GH_TOKEN", "owner-token")

	shellEnv := envFor(runner.Spec{Harness: model.HarnessShell}, launch{})
	if !hasEnv(shellEnv, "SSH_AUTH_SOCK", "/tmp/agent.sock") || !hasEnv(shellEnv, "GH_TOKEN", "owner-token") {
		t.Fatalf("the owner's shell lost its own environment: %v", shellEnv)
	}

	claudeEnv := envFor(runner.Spec{Harness: model.HarnessClaude}, launch{})
	if hasEnv(claudeEnv, "SSH_AUTH_SOCK", "/tmp/agent.sock") || hasEnv(claudeEnv, "GH_TOKEN", "owner-token") {
		t.Fatalf("an agent harness kept the owner's credentials: %v", claudeEnv)
	}
}

func TestChildEnvDropsInheritedFleetIdentity(t *testing.T) {
	t.Setenv("TMX_URL", "http://127.0.0.1:8777")
	t.Setenv("TMX_AGENT_TOKEN", "parent-agent-fixture")
	t.Setenv("TMX_RELAY_TOKEN", "parent-relay-fixture")
	t.Setenv("TMX_HOST", "hostb")
	for _, h := range []model.Harness{model.HarnessShell, model.HarnessClaude} {
		env := envFor(runner.Spec{Harness: h, Env: []string{"TMXG_AGENT_TOKEN=child-fixture"}}, launch{})
		for _, kv := range env {
			key, _, _ := strings.Cut(kv, "=")
			switch strings.ToUpper(key) {
			case "TMX_URL", "TMX_AGENT_TOKEN", "TMX_RELAY_TOKEN":
				t.Fatalf("%s child inherited the launcher's %s", h, key)
			}
		}
		if !hasEnv(env, "TMXG_AGENT_TOKEN", "child-fixture") {
			t.Fatalf("%s child lost its own TMXG_AGENT_TOKEN", h)
		}
	}
	if !hasEnv(envFor(runner.Spec{Harness: model.HarnessShell}, launch{}), "TMX_HOST", "hostb") {
		t.Fatal("the owner's shell lost a non-credential TMX_ variable")
	}
}

func hasEnv(env []string, key, value string) bool {
	for _, kv := range env {
		if kv == key+"="+value {
			return true
		}
	}
	return false
}

func TestShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh")
	}
	p, err := New().Start(context.Background(), runner.Spec{
		Harness: model.HarnessShell, Command: "/bin/sh", Cwd: t.TempDir(), Cols: 80, Rows: 24,
		Env: []string{"PS1=tmx-test-ready> ", "TMXG_TEST_VAR=from-spec"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := watch(p)
	c.state(t, model.StateStarting)

	var out bytes.Buffer
	c.expect(t, &out, "tmx-test-ready> ")
	if err := p.Send("echo hi-$((40+2)) $TERM $TMXG_TEST_VAR"); err != nil {
		t.Fatal(err)
	}
	// Wait for the next prompt, not just echo's output: macOS's Bash 3.2
	// Readline reads and writes back the window size while preparing input.
	// Resizing between those ioctls can be overwritten with the old size.
	c.expect(t, &out, "hi-42 xterm-256color from-spec\r\ntmx-test-ready> ")
	c.state(t, model.StateWorking)
	if !strings.Contains(string(p.Replay()), "hi-42") {
		t.Fatalf("replay %q", p.Replay())
	}

	if err := p.Resize(100, 40); err != nil {
		t.Fatal(err)
	}
	if err := p.Send("stty size"); err != nil {
		t.Fatal(err)
	}
	c.expect(t, &out, "40 100")

	c.state(t, model.StateIdle) // 2 s of quiet

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-c.exit:
		if ev.ExitCode != 0 {
			t.Fatalf("exit %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no EvExit")
	}
}

func TestStopKillsStubbornProgram(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh")
	}
	// ignores "exit" (it is not a shell reading input), SIGHUP and SIGTERM
	p, err := New().Start(context.Background(), runner.Spec{
		Harness: model.HarnessShell, Command: "/bin/sh",
		ExtraArgs: []string{"-c", "trap '' HUP TERM; echo ready; while :; do sleep 1; done"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := watch(p)
	var out bytes.Buffer
	c.expect(t, &out, "ready")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := p.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el < exitGrace {
		t.Fatalf("killed after %v, before the graces", el)
	}
	if ev := <-c.exit; ev.ExitCode == 0 {
		t.Fatalf("exit %+v", ev)
	}
}

func TestRing(t *testing.T) {
	r := newRing(8)
	r.Write([]byte("abc"))
	if got := string(r.Bytes()); got != "abc" {
		t.Fatal(got)
	}
	r.Write([]byte("defgh"))
	if got := string(r.Bytes()); got != "abcdefgh" {
		t.Fatal(got)
	}
	r.Write([]byte("ij"))
	if got := string(r.Bytes()); got != "cdefghij" {
		t.Fatal(got)
	}
	r.Write([]byte("0123456789"))
	if got := string(r.Bytes()); got != "23456789" {
		t.Fatal(got)
	}
	r.Write([]byte("xyz"))
	if got := string(r.Bytes()); got != "56789xyz" {
		t.Fatal(got)
	}
}

func TestUnknownHarness(t *testing.T) {
	if _, err := New().Start(context.Background(), runner.Spec{Harness: "vim"}); err == nil {
		t.Fatal("want error")
	}
}

// A terminal claude starts only with an isolated CLAUDE_CONFIG_DIR, never on ~/.claude.json.
func TestClaudeRefusedWithoutIsolatedConfig(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	_, err := New().Start(context.Background(), runner.Spec{Harness: model.HarnessClaude, Command: "claude-not-run", Cwd: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "CLAUDE_CONFIG_DIR") && !(runner.Elevated() && strings.Contains(err.Error(), "elevated")) {
		t.Fatalf("want refusal, got %v", err)
	}
}
