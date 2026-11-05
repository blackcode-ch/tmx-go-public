package accounts

import (
	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"github.com/blackcode-ch/tmx-go/internal/sysinfo"
	"os/exec"
)

const createNewProcessGroup = 0x00000200

func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = hiddenexec.Attributes(cmd.SysProcAttr)
	cmd.SysProcAttr.CreationFlags |= createNewProcessGroup
}

// killTree ends the command and what it started, by PID.
func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = sysinfo.KillStarted(cmd.Process)
}

// reap: Windows has no process groups to sweep after exit.
func reap(*exec.Cmd) {}
