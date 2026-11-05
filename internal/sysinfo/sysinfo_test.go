package sysinfo

import (
	"errors"
	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTree(t *testing.T) {
	procs := []Proc{
		{PID: 1, Start: 1, PPID: 0, PGID: 1},
		{PID: 10, Start: 10, PPID: 1, PGID: 10},  // root (an agent, own group)
		{PID: 11, Start: 11, PPID: 10, PGID: 10}, // its child
		{PID: 12, Start: 12, PPID: 11, PGID: 12}, // a grandchild in a new group (a Bash tool)
		{PID: 13, Start: 13, PPID: 12, PGID: 12},
		{PID: 14, Start: 14, PPID: 1, PGID: 12}, // re-parented orphan of group 12
		{PID: 20, Start: 20, PPID: 1, PGID: 20}, // a stranger
		{PID: 21, Start: 21, PPID: 20, PGID: 20},
	}
	var got []int
	for _, p := range Tree(procs, 10) {
		got = append(got, p.PID)
	}
	if got[0] != 10 {
		t.Fatalf("root first, got %v", got)
	}
	slices.Sort(got)
	if want := []int{10, 11, 12, 13}; !slices.Equal(got, want) {
		t.Fatalf("tree = %v, want %v", got, want)
	}
	if Tree(procs, 99) != nil {
		t.Fatal("missing root should give nil")
	}
}

func TestSelf(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("measured on darwin and linux")
	}
	procs, err := Procs()
	if err != nil {
		t.Fatal(err)
	}
	me, ok := find(procs, os.Getpid())
	if !ok || me.PPID != os.Getppid() || me.Start == 0 {
		t.Fatalf("self not in table or wrong: %+v", me)
	}
	if !PerProcess {
		t.Skip("no per-process counters in this build")
	}
	// Burn some CPU, then compare with getrusage: the units must agree.
	end := time.Now().Add(150 * time.Millisecond)
	for x := 0; time.Now().Before(end); x++ {
		_ = x * x
	}
	u, ok := ProcUsage(os.Getpid())
	if !ok {
		t.Fatal("no usage for self")
	}
	self := SelfCPU()
	if u.CPU <= 0 || self <= 0 {
		t.Fatalf("cpu %v / rusage %v", u.CPU, self)
	}
	if r := float64(u.CPU) / float64(self); r < 0.7 || r > 1.3 {
		t.Fatalf("ProcUsage CPU %v disagrees with getrusage %v", u.CPU, self)
	}
	if u.Mem < 1<<20 {
		t.Fatalf("memory %d bytes is implausible", u.Mem)
	}
	m, err := MachineStats()
	if err != nil {
		t.Fatal(err)
	}
	if m.CPUTotal <= 0 || m.CPUBusy <= 0 || m.CPUBusy > m.CPUTotal || m.MemTotal == 0 || m.MemFree == 0 || m.MemFree > m.MemTotal {
		t.Fatalf("machine stats implausible: %+v", m)
	}
	if _, err := Disks(); err != nil {
		t.Fatal(err)
	}
	free, total, err := DiskSpace(t.TempDir())
	if err != nil || free == 0 || total < free {
		t.Fatalf("disk space %d/%d %v", free, total, err)
	}
}

func psState(t *testing.T, pid int) string {
	out, err := hiddenexec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// TestSuspendChild pauses and resumes a child process tree this test started itself.
func TestSuspendChild(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires identity-pinned Linux process control")
	}
	cmd := hiddenexec.Command("/bin/sh", "-c", "sleep 30 & wait")
	cmd.Dir = t.TempDir()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = SuspendTree(cmd.Process.Pid, false)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	RegisterChild(cmd.Process.Pid, false)
	time.Sleep(100 * time.Millisecond)
	if err := LowerPriority(cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	got, err := SuspendTree(cmd.Process.Pid, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 {
		t.Fatalf("expected the shell and its sleep, got %+v", got)
	}
	if s := psState(t, cmd.Process.Pid); !strings.HasPrefix(s, "T") {
		t.Fatalf("shell state %q, want stopped", s)
	}
	if _, err := SuspendTree(cmd.Process.Pid, false); err != nil {
		t.Fatal(err)
	}
	if s := psState(t, cmd.Process.Pid); strings.HasPrefix(s, "T") {
		t.Fatalf("shell state %q after resume", s)
	}
	if !strings.Contains(psState(t, cmd.Process.Pid), "N") && runtime.GOOS == "darwin" {
		t.Fatalf("expected a niced process, state %q", psState(t, cmd.Process.Pid))
	}
	if !Alive(cmd.Process.Pid) || Alive(1<<22+12345) {
		t.Fatal("Alive is wrong")
	}
}

func TestSuspendRefusesStrangers(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires identity-pinned Linux process control")
	}
	// PID 1 is never our child: nothing may be signalled.
	if _, err := SuspendTree(1, true); !errors.Is(err, ErrNotChild) {
		t.Fatalf("err = %v, want ErrNotChild", err)
	}
}

// The low-priority wrapper must run the same program with the same arguments, exit code and
// PID (taskpolicy execs it), so the fleet's PID bookkeeping and pausing stay right.
func TestLowPriorityCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		p, a := LowPriorityCommand(`C:\x.exe`, []string{"a"})
		if p != `C:\x.exe` || len(a) != 1 {
			t.Fatalf("windows must not wrap: %s %v", p, a)
		}
		return
	}
	path, args := LowPriorityCommand("/bin/sh", []string{"-c", `echo "$$ $1"; exit 7`, "sh", "two words"})
	if runtime.GOOS == "darwin" && !strings.HasSuffix(path, "taskpolicy") {
		t.Fatalf("macOS wraps with taskpolicy, got %s", path)
	}
	cmd := hiddenexec.Command(path, args...)
	out, err := cmd.Output()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 7 {
		t.Fatalf("exit: %v (out %q)", err, out)
	}
	f := strings.Fields(string(out))
	if len(f) != 3 || f[0] != strconv.Itoa(cmd.Process.Pid) || f[1]+" "+f[2] != "two words" {
		t.Fatalf("output %q; want pid %d and the argument intact", out, cmd.Process.Pid)
	}
}
