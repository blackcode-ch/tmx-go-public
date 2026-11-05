//go:build !windows

package harness

import "path/filepath"

// finalCodexPath returns the real, fully-resolved path for dir.
func finalCodexPath(dir string) (string, error) {
	return filepath.EvalSymlinks(dir)
}
