//go:build !windows

package summarize

import (
	"os/exec"
	"syscall"
)

// claude runs in a process group of its own, so a timeout ends whatever it started too.
func setProcAttr(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func killTree(cmd *exec.Cmd) {
	if cmd.Process != nil && cmd.Process.Pid > 0 {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// reap ends what is left of the group after claude exited. The group id stays reserved while
// any member lives, so this cannot hit an unrelated process.
func reap(cmd *exec.Cmd) { killTree(cmd) }
