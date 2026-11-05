//go:build linux

package sysinfo

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
)

// This uses only this test binary's children and files under t.TempDir(). It
// proves that the real helper survives its parent's exit, without starting tmx.
func TestWatchdogSurvivesParentExit(t *testing.T) {
	dir := t.TempDir()
	cmd := hiddenexec.Command(os.Args[0], "-test.run=^TestWatchdogProcessFixture$")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TMX_WATCHDOG_TEST_ROLE=parent", "TMX_WATCHDOG_TEST_DIR="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "resumed")); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("real watchdog did not resume the test child after parent exit")
}

func TestWatchdogProcessFixture(t *testing.T) {
	role := os.Getenv("TMX_WATCHDOG_TEST_ROLE")
	if role == "" {
		t.Skip("subprocess fixture")
	}
	dir := os.Getenv("TMX_WATCHDOG_TEST_DIR")
	if role == "child" {
		if err := os.WriteFile(filepath.Join(dir, "ready"), []byte("ready"), 0600); err != nil {
			os.Exit(2)
		}
		time.Sleep(time.Second)
		if err := os.WriteFile(filepath.Join(dir, "resumed"), []byte("resumed"), 0600); err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
	cmd := hiddenexec.Command(os.Args[0], "-test.run=^TestWatchdogProcessFixture$")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TMX_WATCHDOG_TEST_ROLE=child")
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(4)
	}
	RegisterChild(cmd.Process.Pid, false)
	deadline := time.Now().Add(3 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
			ready = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		_ = cmd.Process.Kill()
		os.Exit(5)
	}
	if _, err := SuspendTree(cmd.Process.Pid, true); err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = cmd.Process.Kill()
		os.Exit(6)
	}
	assertStopped(t, cmd.Process.Pid)
	// Deliberately no resume, Wait or orderly shutdown: the helper must act alone.
	os.Exit(0)
}

func assertStopped(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		f, ok := statFields(strconv.Itoa(pid))
		if ok && len(f) > 0 && f[0] == "T" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("fixture was not actually stopped")
}

func TestPausedResumedWhenWatchdogIsKilled(t *testing.T) {
	dir := t.TempDir()
	cmd := hiddenexec.Command(os.Args[0], "-test.run=^TestWatchdogProcessFixture$")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TMX_WATCHDOG_TEST_ROLE=child", "TMX_WATCHDOG_TEST_DIR="+dir)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	RegisterChild(cmd.Process.Pid, false)
	if _, err := SuspendTree(cmd.Process.Pid, true); err != nil {
		t.Fatal(err)
	}
	assertStopped(t, cmd.Process.Pid)
	leases.Lock()
	lease := leases.byRoot[cmd.Process.Pid]
	leases.Unlock()
	if lease == nil {
		t.Fatal("missing lease")
	}
	if err := lease.process.Kill(); err != nil {
		t.Fatal(err)
	} // only the helper this test started
	select {
	case <-lease.done:
	case <-time.After(5 * time.Second):
		t.Fatal("helper exit not observed")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "resumed")); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("daemon did not resume child after watchdog was killed")
}
