package transfers

import (
	"path/filepath"
	"syscall"
)

// longPath expands 8.3 aliases without following links. In particular, export
// revalidation must not resolve its saved path and hide a redirected parent.
func longPath(p string) string {
	p = filepath.Clean(p)
	name, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return p
	}
	buf := make([]uint16, 260)
	for {
		n, err := syscall.GetLongPathName(name, &buf[0], uint32(len(buf)))
		if err != nil || n == 0 {
			return p
		}
		if int(n) < len(buf) {
			return filepath.Clean(syscall.UTF16ToString(buf[:n]))
		}
		buf = make([]uint16, int(n)+1)
	}
}
