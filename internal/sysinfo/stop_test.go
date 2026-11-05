package sysinfo

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
)

// Only this test binary's disposable processes are started; the watchdogCommand
// settings also keep Windows test fixtures hidden. No daemon or installer runs.
func TestStopStartedRootDespiteMissingRegistrationOrExemption(t *testing.T) {
	for _, exempt := range []bool{false, true} {
		t.Run(strconv.FormatBool(exempt), func(t *testing.T) {
			cmd := startStopFixture(t, false)
			if exempt {
				RegisterChild(cmd.Process.Pid, true)
			}
			_ = KillStarted(cmd.Process)
			done := make(chan struct{})
			go func() { _ = cmd.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("started root survived Stop")
			}
		})
	}
}

func TestStopRegisteredTree(t *testing.T) {
	if !pinnedControlAvailable {
		t.Skip("no pinned control backend")
	}
	cmd := startStopFixture(t, true)
	RegisterChild(cmd.Process.Pid, false)
	b, err := os.ReadFile(filepath.Join(cmd.Dir, "child-pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(b))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := Procs()
	if err != nil {
		t.Fatal(err)
	}
	child, ok := find(rows, pid)
	if !ok {
		t.Fatal("fixture child missing")
	}
	handle, err := openIdentity(child)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.close()
	defer handle.kill()
	_ = KillStarted(cmd.Process)
	_ = cmd.Wait()
	deadline := time.Now().Add(3 * time.Second)
	for handle.alive() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if handle.alive() {
		t.Fatal("registered descendant survived Stop")
	}
}

func startStopFixture(t *testing.T, tree bool) *exec.Cmd {
	t.Helper()
	dir := t.TempDir()
	cmd := hiddenexec.Command(os.Args[0], "-test.run=^TestStopProcessFixture$")
	watchdogCommand(cmd)
	cmd.Dir = dir
	cmd.Env = append(cmd.Env, "TMX_STOP_FIXTURE=1", "TMX_STOP_TREE="+strconv.FormatBool(tree))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
			return cmd
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("fixture did not start")
	return nil
}

func TestStopProcessFixture(t *testing.T) {
	if os.Getenv("TMX_STOP_FIXTURE") != "1" {
		t.Skip("subprocess fixture")
	}
	if os.Getenv("TMX_STOP_TREE") == "true" {
		cmd := hiddenexec.Command(os.Args[0], "-test.run=^TestStopProcessFixture$")
		watchdogCommand(cmd)
		cmd.Env = append(cmd.Env, "TMX_STOP_FIXTURE=1")
		if err := cmd.Start(); err != nil {
			os.Exit(2)
		}
		if err := os.WriteFile("child-pid", []byte(strconv.Itoa(cmd.Process.Pid)), 0600); err != nil {
			os.Exit(3)
		}
	}
	if err := os.WriteFile("ready", []byte("ready"), 0600); err != nil {
		os.Exit(4)
	}
	time.Sleep(10 * time.Second)
	os.Exit(0)
}
