//go:build !windows

package accounts

import (
	"os/exec"
	"syscall"
)

// A harness command runs in a process group of its own, so a timeout ends what it started too.
func setProcAttr(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func killTree(cmd *exec.Cmd) {
	if cmd.Process != nil && cmd.Process.Pid > 0 {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// reap ends what is left of the group after the command exited (the group id stays reserved
// while any member lives, so this cannot hit an unrelated process).
func reap(cmd *exec.Cmd) { killTree(cmd) }
