//go:build !windows

package host

import (
	"os/exec"
	"syscall"
)

// detach gives the host a session of its own: no controlling terminal, out of the daemon's
// process group.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

func withoutBreakaway(*exec.Cmd) *exec.Cmd { return nil }

// setGroup starts a piped program as the leader of a process group, so ending it ends what it
// started too.
func setGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func signalGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, sig)
}

func terminateGroup(cmd *exec.Cmd) { signalGroup(cmd, syscall.SIGTERM) }
func killGroup(cmd *exec.Cmd)      { signalGroup(cmd, syscall.SIGKILL) }

// reapGroup ends whatever a piped program left running in its process group once it exited.
func reapGroup(cmd *exec.Cmd) { signalGroup(cmd, syscall.SIGKILL) }
