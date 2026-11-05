//go:build linux

package sysinfo

import (
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

type linuxProcess struct{ fd int }

func openIdentity(p Proc) (controlledProcess, error) {
	fd, err := unix.PidfdOpen(p.PID, 0)
	if err != nil {
		return nil, err
	}
	f, ok := statFields(strconv.Itoa(p.PID))
	var start int64
	if ok && len(f) > 19 {
		start, _ = strconv.ParseInt(f[19], 10, 64)
	}
	if p.Start <= 0 || start != p.Start {
		unix.Close(fd)
		return nil, ErrNotChild
	}
	return &linuxProcess{fd}, nil
}
func (p *linuxProcess) suspend() error { return unix.PidfdSendSignal(p.fd, unix.SIGSTOP, nil, 0) }
func (p *linuxProcess) resume() error  { return unix.PidfdSendSignal(p.fd, unix.SIGCONT, nil, 0) }
func (p *linuxProcess) kill() error    { return unix.PidfdSendSignal(p.fd, unix.SIGKILL, nil, 0) }
func (p *linuxProcess) close()         { unix.Close(p.fd) }
func watchdogCommand(cmd *exec.Cmd) {
	cmd.Env = []string{}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func openControlled(p Proc) (controlledProcess, error) {
	if ProtectedProcess(p.Name) {
		return nil, ErrProtected
	}
	return openIdentity(p)
}

func (p *linuxProcess) alive() bool {
	fds := []unix.PollFd{{Fd: int32(p.fd), Events: unix.POLLIN}}
	n, e := unix.Poll(fds, 0)
	return e == nil && n == 0
}

const pinnedControlAvailable = true

func openResume(want Proc) (controlledProcess, error) {
	f, ok := statFields(strconv.Itoa(want.PID))
	if !ok || len(f) <= 19 {
		return nil, ErrNotChild
	}
	start, _ := strconv.ParseInt(f[19], 10, 64)
	if want.Start <= 0 || start != want.Start {
		return nil, ErrNotChild
	}
	name, err := os.ReadFile("/proc/" + strconv.Itoa(want.PID) + "/comm")
	if err != nil {
		return nil, err
	}
	return openControlled(Proc{PID: want.PID, Start: start, Name: strings.TrimSpace(string(name))})
}
