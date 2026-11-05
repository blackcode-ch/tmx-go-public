//go:build windows

package processes

import "os"

func graceful(p *os.Process) { _ = p.Signal(os.Interrupt) }
