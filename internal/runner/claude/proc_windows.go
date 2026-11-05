//go:build windows

package claude

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

// Windows has no SIGTERM for console programs; both steps end the tree by PID.
func terminate(cmd *exec.Cmd) { killTree(cmd) }
func kill(cmd *exec.Cmd)      { killTree(cmd) }

// reapGroup: Windows has no process groups to sweep after exit.
func reapGroup(*exec.Cmd) {}

func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = sysinfo.KillStarted(cmd.Process)
}
