//go:build !windows && !darwin

package antechamber

import "os"

// markFromElsewhere: Linux has no "came from another machine" mark.
func markFromElsewhere(string, string) {}

// noExec removes execute bits.
func noExec(path string) { _ = os.Chmod(path, 0o644) }
