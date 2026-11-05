package hiddenexec

import (
	"context"
	"os/exec"
	"syscall"
	"testing"
)

func TestConstructorsHideWindows(t *testing.T) {
	for _, cmd := range []*exec.Cmd{Command("unused.exe", "version"), CommandContext(context.Background(), "unused.exe", "version")} {
		if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow || cmd.SysProcAttr.CreationFlags&0x08000000 == 0 {
			t.Fatalf("child can create a visible console: %+v", cmd.SysProcAttr)
		}
		if len(cmd.Args) != 2 || cmd.Args[1] != "version" {
			t.Fatalf("arguments changed: %v", cmd.Args)
		}
	}
}

func TestAttributesPreserveProcessSettings(t *testing.T) {
	a := &syscall.SysProcAttr{CreationFlags: 0x200, CmdLine: "raw command line", NoInheritHandles: true}
	got := Attributes(a)
	if got != a || got.CreationFlags != 0x08000200 || !got.HideWindow || got.CmdLine != "raw command line" || !got.NoInheritHandles {
		t.Fatalf("settings lost: %+v", got)
	}
	if Attributes(got).CreationFlags != got.CreationFlags {
		t.Fatal("not idempotent")
	}
}

func TestConPTYKeepsPseudoConsoleAttachment(t *testing.T) {
	a := ConPTYAttributes(&syscall.SysProcAttr{CreationFlags: 0x08000200, CmdLine: "shim"})
	if !a.HideWindow || a.CreationFlags != 0x200 || a.CmdLine != "shim" {
		t.Fatalf("pseudo-console settings lost: %+v", a)
	}
}

func TestInheritConsoleDoesNotDetachFromTheCaller(t *testing.T) {
	cmd := CommandInheritConsole("unused.exe", "version")
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&0x08000000 != 0 {
		t.Fatalf("console-attached child was given its own hidden console: %+v", cmd.SysProcAttr)
	}
	if len(cmd.Args) != 2 || cmd.Args[1] != "version" {
		t.Fatalf("arguments changed: %v", cmd.Args)
	}
}

func TestInheritConsoleAttributesPreserveProcessSettings(t *testing.T) {
	a := &syscall.SysProcAttr{CreationFlags: 0x08000200, CmdLine: "raw command line", NoInheritHandles: true}
	got := InheritConsoleAttributes(a)
	if got != a || got.CreationFlags != 0x200 || !got.HideWindow || got.CmdLine != "raw command line" || !got.NoInheritHandles {
		t.Fatalf("settings lost or console detached: %+v", got)
	}
}

func TestGUIDoesNotHideTheFirstWindow(t *testing.T) {
	cmd := CommandGUI("unused.exe")
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.HideWindow {
		t.Fatalf("GUI relaunch can come back invisible: %+v", cmd.SysProcAttr)
	}
}

func TestGUIAttributesPreserveProcessSettings(t *testing.T) {
	a := &syscall.SysProcAttr{CreationFlags: 0x200, CmdLine: "raw command line"}
	got := GUIAttributes(a)
	if got != a || got.HideWindow || got.CreationFlags != 0x08000200 || got.CmdLine != "raw command line" {
		t.Fatalf("settings lost or window hidden: %+v", got)
	}
}
