package hiddenexec

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
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
	cp, _ := windows.GetConsoleCP()
	if cmd.SysProcAttr == nil || (cmd.SysProcAttr.CreationFlags&0x08000000 != 0) != (cp == 0) {
		t.Fatalf("wrong flags for caller console CP %d: %+v", cp, cmd.SysProcAttr)
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

func TestDetachedSlotPowerShellHasNoConsole(t *testing.T) {
	const helper = "TMX_TEST_DETACHED_SLOT"
	if os.Getenv(helper) == "1" {
		if ok, _, err := windows.NewLazySystemDLL("kernel32.dll").NewProc("FreeConsole").Call(); ok == 0 {
			t.Fatal(err)
		}
		if cp, _ := windows.GetConsoleCP(); cp != 0 {
			t.Fatalf("test helper has a console: %d", cp)
		}
		system, err := windows.GetSystemDirectory()
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(system, "WindowsPowerShell", "v1.0", "powershell.exe")
		cmd := CommandInheritConsole(path, "-NoProfile", "-NonInteractive", "-Command", "Write-Output slot-child-ok")
		if cmd.SysProcAttr.CreationFlags&0x08000000 == 0 {
			t.Fatal("PowerShell could allocate the default terminal from a detached slot")
		}
		out, err := cmd.CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != "slot-child-ok" {
			t.Fatalf("child output = %q, %v", out, err)
		}
		return
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := Command(exe, "-test.run=^TestDetachedSlotPowerShellHasNoConsole$")
	cmd.Env = append(os.Environ(), helper+"=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("detached slot fixture: %v, %s", err, out)
	}
}
