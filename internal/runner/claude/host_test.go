package claude

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/runner/host"
	"github.com/blackcode-ch/tmx-go/internal/runner/host/hosttest"
)

func testHostMain(args []string) int {
	var err error
	switch {
	case len(args) == 2 && args[0] == "--launch":
		exe, _ := os.Executable() // the tmx-host-* copy (hosttest.Executable)
		err = host.Launch(exe, args[1])
	case len(args) == 1:
		err = host.Run(args[0])
	default:
		err = errors.New("usage")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// Hostb's case: a chat agent (claude stream-json) in a session host. A daemon restart in the
// middle of a turn, with a permission request waiting: the next daemon reattaches to the same
// claude, gets the request again as it was asked, answers it, and the turn finishes; nothing
// claude said is lost or seen twice. Linux and Windows.
func TestHostedChatAgentOutlivesTheDaemon(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "host")
	spec := runner.Spec{Name: "w1", Model: "claude-sonnet-5-5", Command: os.Args[0], Cwd: t.TempDir(),
		Env:     []string{EnvConfigDir + "=" + t.TempDir(), "TMXG_FAKE_CLAUDE=1"},
		HostDir: dir, HostExe: hosttest.Executable(t), HostToken: "agent-cred-1"}
	if runner.Elevated() {
		t.Skip("protocol integration requires an unelevated runner")
	}
	p1, err := New().Start(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	w := &watcher{t: t, proc: p1}
	w.until("state:idle") // initialize answered
	if err := p1.Send("run echo hi"); err != nil {
		t.Fatal(err)
	}
	if ev := w.until("session:"); ev.SessionID != "fake-session" {
		t.Fatalf("session %q", ev.SessionID)
	}
	w.until("approval:perm-1:Bash")
	w.until("state:waiting")
	pid := p1.PID()

	// The first daemon goes away with the request unanswered.
	if !p1.(runner.Detacher).Detach() {
		t.Fatal("not detachable")
	}
	for ev := range p1.Events() {
		if ev.Kind == runner.EvExit {
			t.Fatalf("a detach reported an exit: %+v", ev)
		}
	}

	// The next daemon.
	p2, cred, err := New().(runner.Reattacher).Reattach(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		go func() {
			for range p2.Events() {
			}
		}()
		_ = p2.Stop(ctx)
	})
	if cred != "agent-cred-1" || p2.PID() != pid {
		t.Fatalf("credential %q, pid %d (was %d)", cred, p2.PID(), pid)
	}
	w2 := &watcher{t: t, proc: p2}
	w2.until("state:waiting")
	ap := w2.until("approval:perm-1:Bash").Approval // asked again, with its tool
	if err := p2.Answer(ap.ID, runner.Decision{Allow: true}); err != nil {
		t.Fatal(err)
	}
	w2.until("timeline:tool_result:ran: echo hi")
	w2.until("timeline:result:done")
	w2.until("state:idle")
	for _, d := range w2.seen {
		if d == "timeline:tool_use:Bash" {
			t.Fatalf("output the first daemon handled came again: %q", w2.seen)
		}
	}

	// A real stop ends claude and its host.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = p2.Stop(ctx) }()
	if ev := w2.until("exit"); ev.ExitCode != 0 {
		t.Fatalf("exit %+v", ev)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "host.json")); errors.Is(err, os.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the host did not end after claude")
		}
		time.Sleep(100 * time.Millisecond)
	}
}
