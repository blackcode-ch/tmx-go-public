//go:build !windows

package codexguard

import "os"

// Non-Windows pins only support fixture verification, not Windows admission.
func openPinned(path string) (*os.File, error) { return os.Open(path) }
