//go:build windows

package term

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pty "github.com/aymanbagabas/go-pty"
	"github.com/blackcode-ch/tmx-go/internal/model"
	"github.com/blackcode-ch/tmx-go/internal/runner"
)

// A .cmd is what npm installs (opencode, claude, ...); CreateProcess cannot start one
// directly, so term must run it through cmd.exe with its arguments intact.
func TestBatchShimStartsThroughCmd(t *testing.T) {
	dir := t.TempDir()
	shim := filepath.Join(dir, "hello shim.cmd") // a space exercises cmd's quoting
	script := "@echo off\r\n" +
		"echo hello-from-batch\r\n" +
		"echo arg=%~1\r\n"
	if err := os.WriteFile(shim, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := New().Start(context.Background(), runner.Spec{
		Harness: model.HarnessShell, Command: shim, Cwd: dir, Cols: 80, Rows: 24,
		ExtraArgs: []string{"some value"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = p.Stop(ctx)
	}()
	var out strings.Builder
	deadline := time.After(15 * time.Second)
	for !strings.Contains(out.String(), "arg=some value") {
		select {
		case ev, open := <-p.Events():
			if !open {
				t.Fatalf("events ended: %q", out.String())
			}
			if ev.Kind == runner.EvOutput {
				out.Write(ev.Output)
			}
		case <-deadline:
			t.Fatalf("no output from the batch shim: %q", out.String())
		}
	}
	if !strings.Contains(out.String(), "hello-from-batch") {
		t.Fatalf("output: %q", out.String())
	}
}

// Both native executables and batch launchers must retain the hidden flags.
func TestPrepareCommandHidesWindows(t *testing.T) {
	for _, path := range []string{"tool.exe", "tool.cmd", "tool.bat"} {
		cmd := &pty.Cmd{Path: path, Args: []string{path, "some value"}}
		prepareCommand(cmd)
		if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow || cmd.SysProcAttr.CreationFlags&0x08000000 != 0 {
			t.Fatalf("%s: visible child: %+v", path, cmd.SysProcAttr)
		}
		if path != "tool.exe" && !strings.Contains(cmd.SysProcAttr.CmdLine, "some value") {
			t.Fatalf("lost shim command: %+v", cmd)
		}
	}
}
