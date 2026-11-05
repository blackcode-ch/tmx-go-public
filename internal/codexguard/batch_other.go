//go:build !windows

package codexguard

import (
	"fmt"
	"os/exec"
)

func batchCommand(shell, path string, args []string) (*exec.Cmd, error) {
	return nil, fmt.Errorf("Windows batch adapter is unavailable")
}
