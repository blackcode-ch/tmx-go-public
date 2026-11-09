package transfers

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func canonicalOSPrefix(p string) (string, error) {
	p, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	prefix, target := fixedOSPrefix("darwin", p)
	if prefix == "" {
		return p, nil
	}
	// Resolve only the fixed alias, never its user-controlled descendants (which
	// may not exist yet). An unexpected OS layout fails closed.
	real, err := filepath.EvalSymlinks(prefix)
	if err != nil {
		return "", err
	}
	if real != target {
		return "", fmt.Errorf("unexpected OS prefix target: %s", prefix)
	}
	for _, root := range []string{"/", "/private", target} {
		st, err := os.Lstat(root)
		if err != nil {
			return "", err
		}
		owner, ok := st.Sys().(*syscall.Stat_t)
		if !st.IsDir() || !ok || owner.Uid != 0 || (root != target && st.Mode().Perm()&0022 != 0) {
			return "", fmt.Errorf("untrusted OS prefix directory: %s", root)
		}
	}
	return target + strings.TrimPrefix(p, prefix), nil
}
