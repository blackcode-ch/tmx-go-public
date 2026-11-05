package sysinfo

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
)

func TestTreeExcludesReusedPPID(t *testing.T) {
	rows := []Proc{{PID: 10, PPID: 1, Start: 100}, {PID: 11, PPID: 10, Start: 110}, {PID: 12, PPID: 11, Start: 50}, {PID: 13, PPID: 12, Start: 120}, {PID: 14, PPID: 10}, {PID: 15, PPID: 1, PGID: 10, Start: 120}}
	var got []int
	for _, p := range Tree(rows, 10) {
		got = append(got, p.PID)
	}
	if !slices.Equal(got, []int{10, 11}) {
		t.Fatalf("unsafe tree: %v", got)
	}
	if got := Tree([]Proc{{PID: 10}}, 10); len(got) != 0 {
		t.Fatalf("unknown identity: %v", got)
	}
}

func TestProtectedTreeAndIdentity(t *testing.T) {
	daemon := Proc{PID: 1, Start: 1, Name: "tmxg.exe"}
	root := Proc{PID: 10, PPID: 1, Start: 10, Name: "agent.exe"}
	rows := []Proc{daemon, root, {PID: 11, PPID: 10, Start: 11, Name: "Explorer.EXE"}, {PID: 12, PPID: 11, Start: 12, Name: "tool.exe"}, {PID: 13, PPID: 10, Start: 13, Name: "tool.exe"}}
	got, err := captureControl(rows, root, daemon)
	if err != nil || len(got) != 2 || got[1].PID != 13 {
		t.Fatalf("protected subtree admitted: %+v %v", got, err)
	}
	for _, name := range []string{"explorer.exe", `C:\Windows\explorer.exe`, "tailscaled", "sshd", "tmxg", "WindowsTerminal.exe", ""} {
		if !ProtectedProcess(name) {
			t.Errorf("not protected: %q", name)
		}
	}
	root.Start++
	if _, err := captureControl(rows, root, daemon); err == nil {
		t.Fatal("reused root accepted")
	}
	root = rows[1]
	daemon.Start++
	if _, err := captureControl(rows, root, daemon); err == nil {
		t.Fatal("reused daemon accepted")
	}
}

type fakeControlled struct {
	stops, resumes, kills int
	fail                  bool
}

func (*fakeControlled) alive() bool { return true }
func (p *fakeControlled) suspend() error {
	if p.fail {
		return errors.New("failed")
	}
	p.stops++
	return nil
}
func (p *fakeControlled) resume() error { p.resumes++; return nil }
func (p *fakeControlled) kill() error {
	p.kills++
	if p.fail {
		return errors.New("kill failed")
	}
	return nil
}
func (*fakeControlled) close() {}

func TestPausedResumedByWatchdogWhenDaemonDies(t *testing.T) {
	for _, reason := range []string{"daemon death", "deadline", "manual", "ack failure", "partial failure"} {
		t.Run(reason, func(t *testing.T) {
			a, b := &fakeControlled{}, &fakeControlled{fail: reason == "partial failure"}
			release, dead := make(chan struct{}), make(chan struct{})
			deadline := make(chan time.Time, 1)
			ready := func() error {
				switch reason {
				case "daemon death":
					close(dead)
				case "deadline":
					deadline <- time.Now()
				case "manual":
					close(release)
				case "ack failure":
					return errors.New("broken pipe")
				}
				return nil
			}
			err := runPauseLease([]controlledProcess{a, b}, ready, release, dead, deadline)
			if (reason == "ack failure" || reason == "partial failure") && err == nil {
				t.Fatal("failure not reported")
			}
			if a.stops != 1 || a.resumes != 1 || b.resumes != b.stops {
				t.Fatalf("unbalanced resume: %+v %+v", a, b)
			}
		})
	}
}

func TestWatchdogDeadlineWhileAckBlocked(t *testing.T) {
	p := &fakeControlled{}
	deadline := make(chan time.Time, 1)
	unblock := make(chan struct{})
	defer close(unblock)
	err := runPauseLease([]controlledProcess{p}, func() error { deadline <- time.Now(); <-unblock; return nil }, make(chan struct{}), make(chan struct{}), deadline)
	if err == nil || p.resumes != 1 {
		t.Fatalf("blocked acknowledgement defeated watchdog: %v %+v", err, p)
	}
}

func TestWatchdogDoesNotInheritDaemonEnvironment(t *testing.T) {
	cmd := hiddenexec.Command("unused-helper", watchdogArg)
	watchdogCommand(cmd)
	if cmd.Env == nil || len(cmd.Env) != 0 {
		t.Fatal("watchdog inherits daemon environment")
	}
}

func TestKillContinuesAfterDescendantFailure(t *testing.T) {
	root, child := &fakeControlled{}, &fakeControlled{fail: true}
	_ = killPinned([]controlledProcess{root, child})
	if root.kills != 1 || child.kills != 1 {
		t.Fatalf("kill abandoned root: %+v %+v", root, child)
	}
}

func TestKillSkipsExitedDescendant(t *testing.T) {
	daemon := Proc{PID: 1, Start: 1, Name: "tmxg"}
	tree := []Proc{{PID: 10, PPID: 1, Start: 10, Name: "agent"}, {PID: 11, PPID: 10, Start: 11, Name: "tool"}, {PID: 12, PPID: 10, Start: 12, Name: "tool"}}
	rows := append([]Proc{daemon}, tree...)
	handles, err := pinKillCandidates(tree, daemon, func(p Proc) (controlledProcess, error) {
		if p.PID == 11 {
			return nil, errors.New("exited")
		}
		return &fakeControlled{}, nil
	}, func() ([]Proc, error) { return rows, nil })
	if err != nil || len(handles) != 2 {
		t.Fatalf("exited child blocked root/sibling: %d %v", len(handles), err)
	}
}

func TestWatchdogCrashRunsResumeFallback(t *testing.T) {
	calls := 0
	recoverLease(false, func() { calls++ })
	recoverLease(true, func() { calls++ })
	if calls != 1 {
		t.Fatalf("unexpected helper exit lost resume: %d", calls)
	}
}
