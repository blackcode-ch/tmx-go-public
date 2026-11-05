//go:build !windows

package ptyproc

import (
	"syscall"

	pty "github.com/aymanbagabas/go-pty"
)

// Prepare changes nothing outside Windows: executables start as they are.
func Prepare(*pty.Cmd) {}

// AfterStart drops the parent's copy of the tty, so reads on the master end with EIO once
// every program in the terminal has exited, after all output was delivered.
func AfterStart(pt pty.Pty) {
	if u, ok := pt.(pty.UnixPty); ok {
		_ = u.Slave().Close()
	}
}

// go-pty starts the program as a session leader (Setsid), so its pid is the group to signal.
func signalGroup(cmd *pty.Cmd, sigs ...syscall.Signal) {
	if cmd.Process == nil || cmd.Process.Pid <= 0 {
		return
	}
	for _, s := range sigs {
		_ = syscall.Kill(-cmd.Process.Pid, s)
	}
}

// Terminate: interactive shells ignore SIGTERM but not SIGHUP.
func Terminate(cmd *pty.Cmd) { signalGroup(cmd, syscall.SIGHUP, syscall.SIGTERM) }
func Kill(cmd *pty.Cmd)      { signalGroup(cmd, syscall.SIGKILL) }
