//go:build !windows

package codex

import (
	"os/exec"
	"syscall"
)

// setProcAttr puts codex in its own process group so Stop reaches its whole tree.
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func signalGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process != nil && cmd.Process.Pid > 0 {
		_ = syscall.Kill(-cmd.Process.Pid, sig)
	}
}

func terminate(cmd *exec.Cmd) { signalGroup(cmd, syscall.SIGTERM) }
func kill(cmd *exec.Cmd)      { signalGroup(cmd, syscall.SIGKILL) }

// reapGroup kills what is left of the group after the leader exited. The group id stays
// reserved while any member lives, so this cannot hit an unrelated process.
func reapGroup(cmd *exec.Cmd) { signalGroup(cmd, syscall.SIGKILL) }
