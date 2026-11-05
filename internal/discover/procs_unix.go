//go:build !windows

package discover

import (
	"bufio"
	"bytes"
	"errors"
	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// listProcs reads /proc on Linux, `ps` elsewhere (macOS has no /proc).
func listProcs() ([]Proc, error) {
	if runtime.GOOS == "linux" {
		if procs, err := procFS(); err == nil {
			return procs, nil
		}
	}
	out, err := hiddenexec.Command("ps", "-axww", "-o", "pid=,ppid=,lstart=,args=").Output()
	if err != nil {
		return nil, err
	}
	return parsePS(out), nil
}

// parsePS reads `ps -o pid=,ppid=,lstart=,args=` (lstart: "Tue Oct  7 16:00:00 2026").
func parsePS(out []byte) []Proc {
	var procs []Proc
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 8 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		if e1 != nil || e2 != nil {
			continue
		}
		p := Proc{PID: pid, PPID: ppid, Args: f[7:]}
		if t, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(f[2:7], " "), time.Local); err == nil {
			p.Started = t
		}
		procs = append(procs, p)
	}
	return procs
}

func procFS() ([]Proc, error) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	boot := bootTime()
	var procs []Proc
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		dir := filepath.Join("/proc", e.Name())
		stat, err := os.ReadFile(filepath.Join(dir, "stat"))
		if err != nil {
			continue
		}
		i := bytes.LastIndexByte(stat, ')')
		if i < 0 {
			continue
		}
		rest := strings.Fields(string(stat[i+1:]))
		if len(rest) < 20 {
			continue
		}
		p := Proc{PID: pid}
		p.PPID, _ = strconv.Atoi(rest[1])
		if ticks, err := strconv.ParseInt(rest[19], 10, 64); err == nil && !boot.IsZero() {
			p.Started = boot.Add(time.Duration(ticks) * time.Second / 100) // USER_HZ is 100 on Linux
		}
		if cmd, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil {
			for _, a := range bytes.Split(bytes.TrimRight(cmd, "\x00"), []byte{0}) {
				if len(a) > 0 {
					p.Args = append(p.Args, string(a))
				}
			}
		}
		p.Cwd, _ = os.Readlink(filepath.Join(dir, "cwd"))
		procs = append(procs, p)
	}
	return procs, nil
}

func bootTime() time.Time {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "btime "); ok {
			if s, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				return time.Unix(s, 0)
			}
		}
	}
	return time.Time{}
}

// procCwds: macOS has no /proc; lsof reports the working folder of the user's processes.
func procCwds(pids []int) map[int]string {
	out := map[int]string{}
	if runtime.GOOS == "linux" {
		for _, pid := range pids {
			if d, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd"); err == nil {
				out[pid] = d
			}
		}
		return out
	}
	list := make([]string, len(pids))
	for i, p := range pids {
		list[i] = strconv.Itoa(p)
	}
	b, _ := hiddenexec.Command("lsof", "-a", "-d", "cwd", "-p", strings.Join(list, ","), "-Fpn").Output()
	return parseLsof(b)
}

// parseLsof reads `lsof -Fpn`: "p<pid>" then "n<path>" lines.
func parseLsof(b []byte) map[int]string {
	out := map[int]string{}
	pid := 0
	for _, l := range strings.Split(string(b), "\n") {
		switch {
		case strings.HasPrefix(l, "p"):
			pid, _ = strconv.Atoi(l[1:])
		case strings.HasPrefix(l, "n") && pid != 0:
			out[pid] = l[1:]
		}
	}
	return out
}

func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func terminate(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }
