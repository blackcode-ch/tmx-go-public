//go:build windows

package cli

import (
	"os/exec"

	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"golang.org/x/sys/windows"
)

// detach starts the daemon in its own process group with a hidden console (never a window
// on the desktop); the agents it starts inherit that hidden console.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = hiddenexec.Attributes(cmd.SysProcAttr)
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
}
