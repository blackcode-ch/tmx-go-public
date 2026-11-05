package hiddenexec

import (
	"os/exec"
	"syscall"
)

// Attributes hides ordinary child processes, preserving flags and raw command lines.
// Pseudo-console children must use ConPTYAttributes instead.
func Attributes(attr *syscall.SysProcAttr) *syscall.SysProcAttr {
	if attr == nil {
		attr = &syscall.SysProcAttr{}
	}
	attr.HideWindow = true
	attr.CreationFlags |= 0x08000000 // CREATE_NO_WINDOW
	return attr
}

func configure(cmd *exec.Cmd) { cmd.SysProcAttr = Attributes(cmd.SysProcAttr) }

// ConPTYAttributes leaves console attachment to the invisible pseudo-console.
// CREATE_NO_WINDOW prevents attachment and discards terminal output on Windows.
// go-pty passes CreationFlags through but does not currently consume HideWindow;
// its pseudo-console, rather than STARTUPINFO, provides window isolation.
func ConPTYAttributes(attr *syscall.SysProcAttr) *syscall.SysProcAttr {
	attr = Attributes(attr)
	attr.CreationFlags &^= 0x08000000
	return attr
}

// InheritConsoleAttributes keeps a console-attached child off CREATE_NO_WINDOW, which would
// otherwise give it its own hidden console instead of the one it needs to share: breaking
// Ctrl-C/SIGTERM forwarding (Go cannot signal Interrupt across console boundaries on Windows)
// and stranding CONIN$/CONOUT$ programs on a console nobody can see. HideWindow stays set:
// it only hides a window Windows creates for a *new* console, so it is a no-op here, but it
// keeps this call visibly in the same "hidden" family as Attributes.
func InheritConsoleAttributes(attr *syscall.SysProcAttr) *syscall.SysProcAttr {
	if attr == nil {
		attr = &syscall.SysProcAttr{}
	}
	attr.HideWindow = true
	attr.CreationFlags &^= 0x08000000
	return attr
}

func configureInheritConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = InheritConsoleAttributes(cmd.SysProcAttr)
}

// GUIAttributes is for a GUI child meant to be seen: it must not set HideWindow, since Windows
// applies STARTF_USESHOWWINDOW/SW_HIDE to that child's first-ever window, which would leave a
// relaunched GUI invisible with no way to show itself again. CREATE_NO_WINDOW is harmless and
// kept for parity: it only affects console-subsystem processes, which a GUI app is not.
func GUIAttributes(attr *syscall.SysProcAttr) *syscall.SysProcAttr {
	if attr == nil {
		attr = &syscall.SysProcAttr{}
	}
	attr.CreationFlags |= 0x08000000 // CREATE_NO_WINDOW
	return attr
}

func configureGUI(cmd *exec.Cmd) { cmd.SysProcAttr = GUIAttributes(cmd.SysProcAttr) }
