// Package hiddenexec is the single entry point for child process construction.
// Windows children never allocate a console window; other platforms retain exec semantics.
package hiddenexec

import (
	"context"
	"os/exec"
)

func Command(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	configure(cmd)
	return cmd
}

func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	configure(cmd)
	return cmd
}

// CommandInheritConsole is for a child meant to share the invoking console: interactive
// programs, and anything the caller forwards Ctrl-C to. Unlike Command, it must not force
// the child onto its own hidden console, which would break signal delivery and CONIN$/CONOUT$.
func CommandInheritConsole(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	configureInheritConsole(cmd)
	return cmd
}

// CommandGUI is for relaunching a GUI app that must become visible again, such as after an
// update. Unlike Command, it must not set HideWindow: Windows applies that to the app's
// first window ever shown, which would leave it invisible from then on.
func CommandGUI(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	configureGUI(cmd)
	return cmd
}
