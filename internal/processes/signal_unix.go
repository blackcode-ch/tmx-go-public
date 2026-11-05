//go:build !windows

package processes

import (
	"os"
	"syscall"
)

func graceful(p *os.Process) { _ = p.Signal(syscall.SIGTERM) }
