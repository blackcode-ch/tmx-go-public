package term

import pty "github.com/aymanbagabas/go-pty"

// What another package needs to run a program in a pty the way terminal agents run, without
// a harness watcher (internal/accounts: the harnesses' own sign-in in a terminal).

// Ring keeps the last bytes written to it: a terminal's replay.
type Ring = ring

// NewRing returns a Ring that keeps the last size bytes.
func NewRing(size int) *Ring { return newRing(size) }

// PrepareCommand makes cmd runnable in the pty (Windows: npm's .cmd shims through cmd.exe).
func PrepareCommand(cmd *pty.Cmd) { prepareCommand(cmd) }

// AfterStart drops the parent's copy of the tty (Unix), so reads end once the program exited.
func AfterStart(pt pty.Pty) { afterStart(pt) }

// Terminate asks the program and what it started to end (SIGHUP and SIGTERM to its session).
func Terminate(cmd *pty.Cmd) { terminate(cmd) }

// Kill ends the program and what it started.
func Kill(cmd *pty.Cmd) { kill(cmd) }
