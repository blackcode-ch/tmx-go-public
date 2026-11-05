//go:build windows

package update

import (
	"os/exec"

	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
)

// hideWindow keeps a helper (gh) from opening a console window on the owner's desktop.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = hiddenexec.Attributes(cmd.SysProcAttr)
}
