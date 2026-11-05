package processes

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/runner/host"
	"github.com/blackcode-ch/tmx-go/internal/runner/host/hosttest"
)

// The test binary is also `tmx host [--launch] DIR` and, with PROCTEST_TICK_FILE set, a tool
// that writes a counter to that file every 100 ms (a watcher or preview server stand-in).
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "host" {
		var err error
		switch args := os.Args[2:]; {
		case len(args) == 2 && args[0] == "--launch":
			exe, _ := os.Executable()
			err = host.Launch(exe, args[1])
		case len(args) == 1:
			err = host.Run(args[0])
		default:
			err = errors.New("usage")
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if f := os.Getenv("PROCTEST_TICK_FILE"); f != "" {
		for i := 0; ; i++ {
			_ = os.WriteFile(f, []byte(strconv.Itoa(i)), 0o600)
			time.Sleep(100 * time.Millisecond)
		}
	}
	os.Exit(m.Run())
}

func ticks(t *testing.T, f string) int {
	t.Helper()
	b, _ := os.ReadFile(f)
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

// waitTicking waits until the tool's counter moves past from.
func waitTicking(t *testing.T, f string, from int, what string) int {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if n := ticks(t, f); n > from {
			return n
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: the tool's counter did not move past %d", what, from)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// A daemon restart: the first registry detaches from an agent's tool, which keeps running; the
// next adopts the very same process (same PID, same owner) and can still stop it. Linux and
// Windows (tmx-lead: preview servers and watchers must survive restarts).
func TestHostedToolOutlivesTheDaemon(t *testing.T) {
	tick := filepath.Join(t.TempDir(), "tick")
	t.Setenv("PROCTEST_TICK_FILE", tick)
	dir := filepath.Join(t.TempDir(), "processes")
	exe := hosttest.Executable(t)

	r1 := New()
	r1.EnableHosts(exe, dir)
	info, err := r1.Start(Spec{OwnerID: "agent-1", ProjectID: "p1", Purpose: "watch the build", Command: os.Args[0], Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	n := waitTicking(t, tick, -1, "started")
	if info.PID <= 0 || info.State != "running" {
		t.Fatalf("info %+v", info)
	}

	r1.Detach()                                       // the daemon stops
	n = waitTicking(t, tick, n+2, "after the detach") // still counting with no daemon

	r2 := New()
	r2.EnableHosts(exe, dir)
	adopted := r2.Adopt()
	if len(adopted) != 1 || adopted[0].ID != info.ID || adopted[0].PID != info.PID || adopted[0].OwnerID != "agent-1" || adopted[0].Purpose != "watch the build" {
		t.Fatalf("adopted %+v, was %+v", adopted, info)
	}
	if l := r2.List(); len(l) != 1 || l[0].State != "running" {
		t.Fatalf("list %+v", l)
	}
	n = waitTicking(t, tick, n, "after the adopt")

	r2.StopOwner("agent-1") // the agent stops: its tools with it
	stopped := ticks(t, tick)
	time.Sleep(600 * time.Millisecond)
	if after := ticks(t, tick); after > stopped+1 {
		t.Fatalf("the tool still runs after a stop: %d -> %d", stopped, after)
	}
	if _, err := os.Stat(filepath.Join(dir, info.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the tool's folder was kept: %v", err)
	}
	if l := r2.List(); len(l) != 1 || l[0].State != "stopped" {
		t.Fatalf("after the stop: %+v", l)
	}
}

// A folder whose host is gone (killed, a reboot) is forgotten, not adopted; one that is not
// ours is removed; nothing is started again by Adopt.
func TestAdoptForgetsGoneHosts(t *testing.T) {
	dir := t.TempDir()
	gone := filepath.Join(dir, "gone1")
	if err := os.MkdirAll(filepath.Join(gone, "host"), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(gone, "meta.json"), []byte(`{"info":{"id":"gone1","owner_id":"a"},"spec":{"OwnerID":"a","Purpose":"x","Command":"x"}}`), 0o600)
	junk := filepath.Join(dir, "junk")
	_ = os.MkdirAll(junk, 0o700)
	r := New()
	r.EnableHosts(os.Args[0], dir)
	if got := r.Adopt(); len(got) != 0 {
		t.Fatalf("adopted %+v", got)
	}
	for _, p := range []string{gone, junk} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s kept: %v", p, err)
		}
	}
}

// The daemon's own system tools stay its children (it restarts them itself).
func TestSystemToolsAreNotHosted(t *testing.T) {
	r := New()
	r.EnableHosts(os.Args[0], t.TempDir())
	if r.hosted(&entry{spec: Spec{System: true}}) || !r.hosted(&entry{spec: Spec{}}) {
		t.Fatal("hosting rule")
	}
	if (&Registry{}).hosted(&entry{}) {
		t.Fatal("hosted without hosts enabled")
	}
}

// A tool whose host cannot be checked at the daemon's start is listed as "unknown" (never
// started again) and can still be stopped: by its host's recorded identity; an unverifiable
// stop leaves it listed for a later try (o5, #254).
func TestUnknownToolIsListedAndStoppable(t *testing.T) {
	oldDial, oldStop := dialHost, stopHost
	defer func() { dialHost, stopHost = oldDial, oldStop }()
	dialHost = func(string, uint64) (*host.Conn, error) { return nil, host.ErrHostUnknown }
	var stops []string
	stopErr := error(host.ErrHostUnknown)
	stopHost = func(dir string) (bool, error) { stops = append(stops, dir); return false, stopErr }

	dir := t.TempDir()
	tool := filepath.Join(dir, "tool1")
	if err := os.MkdirAll(filepath.Join(tool, "host"), 0o700); err != nil {
		t.Fatal(err)
	}
	m := `{"info":{"id":"tool1","owner_id":"agent-1","purpose":"preview","pid":4242},"spec":{"OwnerID":"agent-1","Purpose":"preview","Command":"x"}}`
	if err := os.WriteFile(filepath.Join(tool, "meta.json"), []byte(m), 0o600); err != nil {
		t.Fatal(err)
	}
	r := New()
	r.EnableHosts(os.Args[0], dir)
	got := r.Adopt()
	if len(got) != 1 || got[0].ID != "tool1" || got[0].State != "unknown" {
		t.Fatalf("adopted %+v", got)
	}
	r.StopOwner("agent-1") // unverifiable: still listed
	if len(stops) != 1 || stops[0] != filepath.Join(tool, "host") {
		t.Fatalf("stops %v", stops)
	}
	if l := r.List(); len(l) != 1 || l[0].State != "unknown" {
		t.Fatalf("after an unverifiable stop: %+v", l)
	}
	stopErr = nil
	r.StopOwner("agent-1") // verified this time
	if l := r.List(); len(stops) != 2 || len(l) != 1 || l[0].State != "stopped" {
		t.Fatalf("after the stop: %v %+v", stops, l)
	}
	if _, err := os.Stat(tool); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("folder kept: %v", err)
	}

	// A daemon that detaches leaves an unknown tool alone.
	if err := os.MkdirAll(filepath.Join(dir, "tool2", "host"), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "tool2", "meta.json"), []byte(strings.ReplaceAll(m, "tool1", "tool2")), 0o600)
	r2 := New()
	r2.EnableHosts(os.Args[0], dir)
	if got := r2.Adopt(); len(got) != 1 {
		t.Fatalf("adopted %+v", got)
	}
	n := len(stops)
	r2.Detach()
	if len(stops) != n {
		t.Fatal("a detach stopped an unknown tool")
	}
}
