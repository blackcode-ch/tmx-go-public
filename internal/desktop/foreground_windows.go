//go:build desktop && windows

package desktop

import "golang.org/x/sys/windows"

var procAllowSetForegroundWindow = windows.NewLazySystemDLL("user32.dll").NewProc("AllowSetForegroundWindow")

// allowForeground lets another process (the daemon, showing the Windows Hello dialog) take the
// foreground. Only the foreground process may grant that; the owner just clicked in this
// window, so it is.
func allowForeground() {
	const asfwAny = ^uintptr(0) // ASFW_ANY
	if procAllowSetForegroundWindow.Find() == nil {
		procAllowSetForegroundWindow.Call(asfwAny)
	}
}
