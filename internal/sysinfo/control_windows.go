//go:build windows

package sysinfo

import (
	"golang.org/x/sys/windows"
	"os/exec"
	"syscall"

	"github.com/blackcode-ch/tmx-go/internal/hiddenexec"
)

type windowsProcess struct{ h windows.Handle }

func openIdentity(p Proc) (controlledProcess, error) { return openWindows(p, false) }

func openWindows(p Proc, legacy bool) (controlledProcess, error) {
	h, err := windows.OpenProcess(processSuspendResume|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(p.PID))
	if err != nil {
		return nil, err
	}
	if !matchesCreation(startTime(h), p.Start, legacy) {
		windows.CloseHandle(h)
		return nil, ErrNotChild
	}
	var name [32768]uint16
	size := uint32(len(name))
	if windows.QueryFullProcessImageName(h, 0, &name[0], &size) != nil {
		windows.CloseHandle(h)
		return nil, ErrProtected
	}
	return &windowsProcess{h}, nil
}
func (p *windowsProcess) suspend() error {
	r, _, _ := procNtSuspendProcess.Call(uintptr(p.h))
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}
func (p *windowsProcess) resume() error {
	r, _, _ := procNtResumeProcess.Call(uintptr(p.h))
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}
func (p *windowsProcess) kill() error { return windows.TerminateProcess(p.h, 1) }
func (p *windowsProcess) close()      { windows.CloseHandle(p.h) }
func watchdogCommand(cmd *exec.Cmd) {
	// The helper needs no daemon credentials or application settings. Go supplies
	// SYSTEMROOT on Windows when required by CreateProcess.
	cmd.Env = []string{}
	cmd.SysProcAttr = hiddenexec.Attributes(cmd.SysProcAttr)
}

func openControlled(p Proc) (controlledProcess, error) {
	h, err := openIdentity(p)
	if err != nil {
		return nil, err
	}
	return protectWindowsHandle(h)
}

func openResume(p Proc) (controlledProcess, error) {
	h, err := openWindows(p, true)
	if err != nil {
		return nil, err
	}
	return protectWindowsHandle(h)
}

func protectWindowsHandle(h controlledProcess) (controlledProcess, error) {
	var name [32768]uint16
	size := uint32(len(name))
	if windows.QueryFullProcessImageName(h.(*windowsProcess).h, 0, &name[0], &size) != nil || ProtectedProcess(windows.UTF16ToString(name[:size])) {
		h.close()
		return nil, ErrProtected
	}
	return h, nil
}

func (p *windowsProcess) alive() bool {
	var code uint32
	return windows.GetExitCodeProcess(p.h, &code) == nil && code == stillActive
}

const pinnedControlAvailable = true

func matchesCreation(current, want int64, legacy bool) bool {
	return current > 0 && want != 0 && (want == current || (legacy && want == int64(uint64(current)*100)))
}
