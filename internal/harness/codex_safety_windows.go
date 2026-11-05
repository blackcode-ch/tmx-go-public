package harness

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

// finalCodexPath returns the real, fully-resolved path for dir: opening the directory itself
// (not following a final symlink blindly) and asking the filesystem for its final path via
// GetFinalPathNameByHandle, which resolves symlinks, junctions (reparse mount points) and 8.3
// short names at every component of the path, not just the last one — unlike
// filepath.EvalSymlinks, which only recognizes IO_REPARSE_TAG_SYMLINK and only resolves a
// reparse point at the end of the path it is given.
func finalCodexPath(dir string) (string, error) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return "", err
	}
	h, err := windows.CreateFile(p,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", dir, err)
	}
	defer windows.CloseHandle(h)

	buf := make([]uint16, 300)
	for {
		n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), 0)
		if err != nil {
			return "", fmt.Errorf("resolve final path for %s: %w", dir, err)
		}
		if int(n) < len(buf) {
			return stripExtendedLengthPrefix(windows.UTF16ToString(buf[:n])), nil
		}
		buf = make([]uint16, n)
	}
}

// stripExtendedLengthPrefix undoes the "\\?\" (and UNC "\\?\UNC\") extended-length prefix that
// GetFinalPathNameByHandle always adds with its default flags, so callers see the same
// drive-letter/UNC form as any other resolved path in this package.
func stripExtendedLengthPrefix(p string) string {
	const uncPrefix = `\\?\UNC\`
	if strings.HasPrefix(p, uncPrefix) {
		return `\\` + p[len(uncPrefix):]
	}
	const extendedPrefix = `\\?\`
	if strings.HasPrefix(p, extendedPrefix) {
		return p[len(extendedPrefix):]
	}
	return p
}
