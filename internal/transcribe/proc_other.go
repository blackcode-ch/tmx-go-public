//go:build !windows

package transcribe

import "os/exec"

func hideWindow(*exec.Cmd) {}
