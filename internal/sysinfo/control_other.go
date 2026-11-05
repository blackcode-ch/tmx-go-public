//go:build !linux && !windows

package sysinfo

import "os/exec"

// No identity-pinned signalling primitive is implemented on this platform.
// Refuse suspension rather than race PID reuse with kill(2).
func openControlled(Proc) (controlledProcess, error) { return nil, ErrUnsupported }
func watchdogCommand(cmd *exec.Cmd)                  { cmd.Env = []string{} }

func openIdentity(Proc) (controlledProcess, error) { return nil, ErrUnsupported }

const pinnedControlAvailable = false

func openResume(Proc) (controlledProcess, error) { return nil, ErrUnsupported }
