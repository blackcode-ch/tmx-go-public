//go:build !windows

package transfers

import "path/filepath"

func longPath(p string) string { return filepath.Clean(p) }
