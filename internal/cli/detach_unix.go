//go:build !windows

package cli

import (
	"os/exec"
	"syscall"
)

// detach runs the daemon in its own session, so it outlives the terminal that started it.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
