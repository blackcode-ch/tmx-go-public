//go:build windows

package host

import (
	"os/exec"

	"golang.org/x/sys/windows"

	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"github.com/blackcode-ch/tmx-go/internal/sysinfo"
)

// detach: no console window (hiddenexec), its own process group, and out of a job object the
// daemon may run in (CREATE_BREAKAWAY_FROM_JOB), so closing that job does not end the agents.
// tmx's resource control does not rely on a job: it tracks the program by its parent (the
// host) and start time (sysinfo.RegisterHosted).
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = hiddenexec.Attributes(cmd.SysProcAttr)
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_BREAKAWAY_FROM_JOB
}

// withoutBreakaway is the same command without CREATE_BREAKAWAY_FROM_JOB, for a job that does
// not allow breaking away (CreateProcess then fails with access denied).
func withoutBreakaway(cmd *exec.Cmd) *exec.Cmd {
	retry := hiddenexec.Command(cmd.Path, cmd.Args[1:]...)
	retry.SysProcAttr = hiddenexec.Attributes(nil)
	retry.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
	return retry
}

// setGroup: a piped program gets its own process group (no console signals from the host's).
func setGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
}

// Windows has no signals for these programs: both steps end the tree, by the started process.
func terminateGroup(cmd *exec.Cmd) { killStarted(cmd) }
func killGroup(cmd *exec.Cmd)      { killStarted(cmd) }

func killStarted(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = sysinfo.KillStarted(cmd.Process)
}

// reapGroup: Windows has no process groups to reap; the program's tree ended with it or was
// ended by a stop.
func reapGroup(*exec.Cmd) {}
