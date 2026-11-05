//go:build !windows

package runner

import "os"

func processElevated() bool { return os.Geteuid() == 0 }
