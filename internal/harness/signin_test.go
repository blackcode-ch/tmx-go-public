package harness

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/blackcode-ch/tmx-go/internal/model"
)

// stub and real are a disabled stub and a real launcher for this platform.
func stubAndReal() (name, stub, real string) {
	if runtime.GOOS == "windows" {
		return "codex.cmd", "@echo off\r\nexit /b 78\r\n", "@echo off\r\nnode codex.js %*\r\n"
	}
	return "codex", "#!/bin/sh\nexit 78\n", "#!/bin/sh\nexec node codex.js \"$@\"\n"
}

// Hostb's PATH: the production stub folder first, the real codex after it (npm's folder).
func TestSignInPathSkipsStub(t *testing.T) {
	name, stub, real := stubAndReal()
	stubs, npm := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(stubs, name), []byte(stub), 0o755)
	os.WriteFile(filepath.Join(npm, name), []byte(real), 0o755)
	t.Setenv("PATH", stubs+string(os.PathListSeparator)+npm)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())

	if p, _ := LookPath(model.HarnessCodex, ""); p != filepath.Join(stubs, name) {
		t.Fatalf("agents must still get the stub first: %s", p)
	}
	p, err := SignInPath(model.HarnessCodex)
	if err != nil || p != filepath.Join(npm, name) {
		t.Fatalf("sign-in found %s %v", p, err)
	}
}

// The stub first, the real codex only in npm's install folder (a daemon whose PATH misses it).
func TestSignInPathFallsBackToInstallDir(t *testing.T) {
	name, stub, real := stubAndReal()
	stubs, home, appdata := t.TempDir(), t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(stubs, name), []byte(stub), 0o755)
	t.Setenv("PATH", stubs)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", appdata)
	dir := filepath.Join(home, ".local", "bin")
	if runtime.GOOS == "windows" {
		dir = filepath.Join(appdata, "npm")
	}
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, name), []byte(real), 0o755)
	if p, err := SignInPath(model.HarnessCodex); err != nil || p != filepath.Join(dir, name) {
		t.Fatalf("sign-in found %s %v", p, err)
	}
}

// Only the stub: it is returned, so the sign-in runs it and says it is disabled.
func TestSignInPathOnlyStub(t *testing.T) {
	name, stub, _ := stubAndReal()
	stubs := t.TempDir()
	os.WriteFile(filepath.Join(stubs, name), []byte(stub), 0o755)
	t.Setenv("PATH", stubs)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	if p, err := SignInPath(model.HarnessCodex); err != nil || p != filepath.Join(stubs, name) {
		t.Fatalf("%s %v", p, err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := SignInPath(model.HarnessCodex); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("nothing: %v", err)
	}
}

func TestIsDisabledStub(t *testing.T) {
	dir := t.TempDir()
	for content, want := range map[string]bool{
		"@echo off\r\nexit /b 78\r\n": true,
		"#!/bin/sh\nexit 78\n":        true,
		"@echo off\r\nexit /b 1\r\n":  false,
		"#!/bin/sh\nexit 780\n":       false,
	} {
		p := filepath.Join(dir, "x")
		os.WriteFile(p, []byte(content), 0o755)
		if IsDisabledStub(p) != want {
			t.Errorf("%q: want %v", content, want)
		}
	}
	big := make([]byte, maxStub+1)
	copy(big, "exit 78\n")
	p := filepath.Join(dir, "big")
	os.WriteFile(p, big, 0o755)
	if IsDisabledStub(p) {
		t.Error("a big file is a real launcher")
	}
}
