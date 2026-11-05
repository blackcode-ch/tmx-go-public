//go:build windows

package transcribe

import (
	"os/exec"

	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
)

// hideWindow keeps the engine (a console program) from opening a window on the owner's desktop.
// Its priority is sysinfo.LowerPriority's job.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = hiddenexec.Attributes(cmd.SysProcAttr)
}
