package term

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
	"github.com/blackcode-ch/tmx-go/internal/runner/host"
	"github.com/blackcode-ch/tmx-go/internal/runner/host/hosttest"
)

// testHostMain is the test binary run as `tmx host [--launch] DIR`.
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

// fakeLine is the program in the host: "ready", then "got:<line>" for every line read.
func fakeLine() {
	fmt.Print("ready\r\n")
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		fmt.Printf("got:%s\r\n", strings.TrimSpace(sc.Text()))
	}
}

// waitOutput reads p's events until out contains want.
func waitOutput(t *testing.T, p runner.Process, out *strings.Builder, want string) {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for !strings.Contains(out.String(), want) {
		select {
		case ev, ok := <-p.Events():
			if !ok {
				t.Fatalf("events closed before %q; output so far %q", want, out.String())
			}
			if ev.Kind == runner.EvOutput {
				out.Write(ev.Output)
			}
		case <-deadline:
			t.Fatalf("no %q within 20 s; output so far %q", want, out.String())
		}
	}
}

// A daemon restart: the first daemon detaches from the session host, the program keeps
// running, and the next daemon reattaches to the very same program: its screen (the replay)
// and input work, and it gets the agent's credential back. Linux and Windows (ConPTY).
func TestSessionHostOutlivesTheDaemon(t *testing.T) {
	t.Setenv("TMXG_FAKE_LINE", "1")
	dir := filepath.Join(t.TempDir(), "host")
	spec := runner.Spec{Harness: model.HarnessShell, Command: os.Args[0], Cwd: t.TempDir(),
		HostDir: dir, HostExe: hosttest.Executable(t), HostToken: "agent-cred-1"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	p1, err := New().Start(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	var out1 strings.Builder
	waitOutput(t, p1, &out1, "ready")
	pid := p1.PID()
	if pid <= 0 {
		t.Fatalf("pid %d", pid)
	}

	// The first daemon goes away.
	if !p1.(runner.Detacher).Detach() {
		t.Fatal("not detachable")
	}
	for ev := range p1.Events() {
		if ev.Kind == runner.EvExit {
			t.Fatalf("a detach reported an exit: %+v", ev)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "host.json")); err != nil {
		t.Fatalf("the host went away with the daemon: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "spec.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the spec (it holds the environment) was left behind: %v", err)
	}

	// The next daemon.
	p2, cred, err := New().(runner.Reattacher).Reattach(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if cred != "agent-cred-1" {
		t.Fatalf("credential %q", cred)
	}
	if p2.PID() != pid {
		t.Fatalf("a different program: %d, was %d", p2.PID(), pid)
	}
	var out2 strings.Builder
	waitOutput(t, p2, &out2, "ready") // the replay
	if err := p2.Write([]byte("hello\r"), true); err != nil {
		t.Fatal(err)
	}
	waitOutput(t, p2, &out2, "got:hello")

	// A real stop still ends it, and the host with it.
	sctx, scancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer scancel()
	if err := p2.Stop(sctx); err != nil {
		t.Fatal(err)
	}
	exited := false
	for ev := range p2.Events() {
		exited = exited || ev.Kind == runner.EvExit
	}
	if !exited {
		t.Fatal("no exit after a stop")
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "host.json")); errors.Is(err, os.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the host did not end after its program")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, _, err := New().(runner.Reattacher).Reattach(ctx, spec); !errors.Is(err, host.ErrNoHost) {
		t.Fatalf("reattach after the end: %v", err)
	}
}
