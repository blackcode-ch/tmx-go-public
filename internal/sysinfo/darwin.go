//go:build darwin

package sysinfo

import (
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// Procs lists every process (one sysctl, pure Go).
func Procs() ([]Proc, error) {
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	out := make([]Proc, 0, len(kps))
	for i := range kps {
		k := &kps[i]
		if k.Proc.P_pid <= 0 {
			continue
		}
		st := k.Proc.P_starttime
		out = append(out, Proc{
			PID: int(k.Proc.P_pid), PPID: int(k.Eproc.Ppid), PGID: int(k.Eproc.Pgid),
			Start: int64(st.Sec)*1_000_000 + int64(st.Usec),
			Name:  unix.ByteSliceToString(k.Proc.P_comm[:]),
		})
	}
	return out, nil
}

func currentNice(prio int) int { return prio }

// lowerIO: macOS can set another process's I/O policy only from inside it
// (setiopolicy_np), so agents keep the default I/O policy; see DESIGN.md "Resources".
func lowerIO(int) error { return nil }

func memTotal() uint64 {
	v, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0
	}
	return v
}

// memFree is what the kernel's memory status counts as available (the percentage that
// memory_pressure prints, over physical memory): free, reclaimable cache and compressible
// pages. Free+inactive alone reads far too low on a Mac that compresses memory.
func memFree() uint64 {
	lvl, err := unix.SysctlUint32("kern.memorystatus_level")
	if err != nil || lvl > 100 {
		return 0
	}
	return memTotal() / 100 * uint64(lvl)
}

func ncpu() int {
	v, err := unix.SysctlUint32("hw.ncpu")
	if err != nil {
		return 1
	}
	return int(v)
}

const taskpolicy = "/usr/sbin/taskpolicy"

var hasTaskpolicy = sync.OnceValue(func() bool {
	st, err := os.Stat(taskpolicy)
	return err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0
})

// wrapLow: setiopolicy_np works only on the calling process, so the agent is started
// through taskpolicy, which sets IOPOL_UTILITY (throttled behind the owner's interactive
// I/O, unlike "throttle" not starved) and execs it.
func wrapLow(path string, args []string) (string, []string) {
	if !hasTaskpolicy() {
		return path, args
	}
	return taskpolicy, append([]string{"-d", "utility", "--", path}, args...)
}
