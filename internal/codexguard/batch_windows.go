//go:build windows

package codexguard

import (
	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"os/exec"
	"syscall"
)

func batchCommand(shell, path string, args []string) (*exec.Cmd, error) {
	line, err := BatchLine(path, args)
	if err != nil {
		return nil, err
	}
	cmd := hiddenexec.Command(shell)
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CmdLine = syscall.EscapeArg(shell) + " /d /s /v:off /c " + line
	return cmd, nil
}
