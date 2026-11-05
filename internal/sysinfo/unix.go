//go:build !windows

package sysinfo

import (
	"errors"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Nice is the CPU niceness agent trees run at (0 normal, 19 lowest).
const Nice = 10

// LowerPriority lowers pid's CPU priority to Nice (and, on Linux, its I/O priority).
// Processes it starts afterwards inherit both. Call it right after the start.
func LowerPriority(pid int) error {
	if pid <= 0 {
		return errors.New("no process")
	}
	// Getpriority returns 20-nice on Linux (raw syscall) and nice on BSD/macOS; only lower.
	if cur, err := unix.Getpriority(unix.PRIO_PROCESS, pid); err == nil && currentNice(cur) >= Nice {
		return lowerIO(pid)
	}
	if err := unix.Setpriority(unix.PRIO_PROCESS, pid, Nice); err != nil {
		return err
	}
	return lowerIO(pid)
}

// Alive reports whether a process with this PID exists.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// DiskSpace is the free (for this user) and total bytes of the volume holding path.
func DiskSpace(path string) (free, total uint64, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)
	return uint64(st.Bavail) * bs, uint64(st.Blocks) * bs, nil
}

// SelfCPU is the CPU time this process used so far (user + system).
func SelfCPU() time.Duration {
	var ru unix.Rusage
	if unix.Getrusage(unix.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
