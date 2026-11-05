package processes

import (
	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/sysinfo"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

func TestProcessHelper(t *testing.T) {
	if os.Args[len(os.Args)-1] == "process-parent" {
		exe, _ := os.Executable()
		child := hiddenexec.Command(exe, "-test.run=^TestProcessHelper$", "--", "process-helper")
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		if err := os.WriteFile(os.Args[len(os.Args)-2], []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
			os.Exit(4)
		}
		_ = child.Wait()
		return
	}
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "process-helper" {
		return
	}
	for {
		time.Sleep(time.Second)
	}
}

func TestStopReapsPinnedDescendants(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skip("identity-pinned tree control supported on Windows and Linux")
	}
	r := New()
	t.Cleanup(r.Close)
	exe, _ := os.Executable()
	pidFile := filepath.Join(t.TempDir(), "child-pid")
	p, err := r.Start(Spec{OwnerID: "a", Purpose: "parent", Command: exe, Args: []string{"-test.run=^TestProcessHelper$", "--", pidFile, "process-parent"}})
	if err != nil {
		t.Fatal(err)
	}
	childPID := 0
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, e := os.ReadFile(pidFile); e == nil {
			childPID, _ = strconv.Atoi(string(b))
			if childPID > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == 0 {
		t.Fatal("child never started")
	}
	r.Stop(p.ID)
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := sysinfo.Procs()
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, v := range rows {
			if v.PID == childPID {
				found = true
			}
		}
		if !found {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("descendant survived graceful parent exit")
}

func helper(t *testing.T, r *Registry, owner string, system bool) model.ProcessInfo {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Start(Spec{OwnerID: owner, Purpose: "test worker", Command: exe, Args: []string{"-test.run=^TestProcessHelper$", "--", "process-helper"}, System: system})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOwnedProcessesStopAndPromotion(t *testing.T) {
	r := New()
	t.Cleanup(r.Close)
	a := helper(t, r, "agent-a", false)
	b := helper(t, r, "agent-a", false)
	if len(r.List()) != 2 || a.PID == 0 || b.PID == 0 {
		t.Fatal("both children must be listed")
	}
	if err := r.Promote(b.ID, "agent-b", "project"); err != nil {
		t.Fatal(err)
	}
	r.StopOwner("agent-a")
	for _, p := range r.List() {
		if p.ID == a.ID && p.PID != 0 {
			t.Fatal("original child survives")
		}
		if p.ID == b.ID && p.PID != b.PID {
			t.Fatal("promoted child was stopped")
		}
	}
	r.StopOwner("agent-b")
	for _, p := range r.List() {
		if p.PID != 0 {
			t.Fatal("owner's child survives")
		}
	}
}

func TestSystemRestartsAfterKill(t *testing.T) {
	r := New()
	t.Cleanup(r.Close)
	p := helper(t, r, "system", true)
	r.mu.Lock()
	cmd := r.entries[p.ID].cmd
	r.mu.Unlock()
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		v := r.List()[0]
		if v.Restarts > 0 && v.PID > 0 && v.PID != p.PID {
			r.StopOwner("system")
			if r.List()[0].PID != v.PID {
				t.Fatal("agent stop affected system tool")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("system process did not restart within one minute")
}

func TestCloseCancelsRestartAndLaunch(t *testing.T) {
	r := New()
	helper(t, r, "system", true)
	r.Close()
	if _, err := r.Start(Spec{OwnerID: "a", Purpose: "x", Command: "x"}); err == nil {
		t.Fatal("start after close")
	}
	for _, p := range r.List() {
		if p.PID != 0 {
			t.Fatal("process survives close")
		}
	}
}

func TestToolEnvironmentDropsIdentity(t *testing.T) {
	got := toolEnv([]string{"PATH=bin", "TMXG_OWNER_TOKEN=secret", "tmx_agent_token=secret", "CLAUDECODE=1"})
	if len(got) != 1 || got[0] != "PATH=bin" {
		t.Fatal("credential environment inherited")
	}
}
