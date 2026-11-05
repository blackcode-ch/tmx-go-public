//go:build !windows

package hiddenexec

import "os/exec"

func configure(*exec.Cmd) {}

func configureInheritConsole(*exec.Cmd) {}

func configureGUI(*exec.Cmd) {}
