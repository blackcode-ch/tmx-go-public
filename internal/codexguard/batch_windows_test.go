//go:build windows

package codexguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBatchAdapterPreservesSpacesAndEmptyArguments(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "path with spaces")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "fixture.cmd")
	if err := os.WriteFile(path, []byte("@echo off\r\necho [%~1][%~2][%~3]\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	shell := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	cmd, err := batchCommand(shell, path, []string{"two words", "", "tail"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if strings.TrimSpace(string(out)) != "[two words][][tail]" {
		t.Fatalf("argv changed: %q", out)
	}
}
