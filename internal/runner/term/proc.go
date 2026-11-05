package term

import (
	pty "github.com/aymanbagabas/go-pty"

	"github.com/blackcode-ch/tmx-go/internal/runner/ptyproc"
)

// The platform's way to run a program in a pty (shared with the session host, runner/host).

func prepareCommand(cmd *pty.Cmd) { ptyproc.Prepare(cmd) }
func afterStart(pt pty.Pty)       { ptyproc.AfterStart(pt) }
func terminate(cmd *pty.Cmd)      { ptyproc.Terminate(cmd) }
func kill(cmd *pty.Cmd)           { ptyproc.Kill(cmd) }
